package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func adminControlRequest(g *Gateway, method, path, body, revision string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/admin/api"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer admin-secret")
	if revision != "" {
		r.Header.Set("If-Match", `"`+revision+`"`)
	}
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, r)
	return w
}

func TestReviewedAccountExitPreservesAdmittedCallsAndRejectsStaleChange(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var firstCalls, secondCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		close(entered)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(completedChat))
	}))
	defer func() { releaseOnce.Do(func() { close(release) }); first.Close() }()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(completedChat))
	}))
	defer second.Close()
	g := newTestGateway(t, []config.Account{
		{ID: "old", Type: "openai", BaseURL: first.URL + "/v1", APIKey: "test", Models: []string{"upstream"}, Enabled: true, Weight: 1, Concurrency: 2},
		{ID: "replacement", Type: "openai", BaseURL: second.URL + "/v1", APIKey: "test", Models: []string{"upstream"}, Enabled: true, Weight: 1, Concurrency: 2},
	}, map[string]config.Route{"stable": {Targets: []config.RouteTarget{{Account: "old", Model: "upstream"}, {Account: "replacement", Model: "upstream"}}}})
	key, secret, err := g.clientKeys.Create(ClientKeyCreate{Name: "my tool", Models: []string{"stable"}, Concurrency: 2, RPM: 20})
	if err != nil {
		t.Fatal(err)
	}
	call := func() *httptest.ResponseRecorder {
		r := gatewayRequest(`{"model":"stable","messages":[{"role":"user","content":"ping"}]}`)
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		g.ServeGateway(w, r)
		return w
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call() }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first source was not called")
	}
	impactResponse := adminControlRequest(g, http.MethodGet, "/accounts/old/impact", "", "")
	var impact accountRemovalImpact
	if err := json.Unmarshal(impactResponse.Body.Bytes(), &impact); err != nil || impactResponse.Code != 200 || impact.Active != 1 || !impact.CanApply || !slices.Contains(impact.AffectedModels, "stable") || len(impact.ClientKeys) != 1 || impact.ClientKeys[0].ID != key.ID {
		t.Fatalf("impact=%+v status=%d err=%v", impact, impactResponse.Code, err)
	}
	updated := g.Config().Accounts[1]
	updated.Name = "changed after review"
	if err := g.UpsertAccount(updated); err != nil {
		t.Fatal(err)
	}
	if w := adminControlRequest(g, http.MethodDelete, "/accounts/old", "", impact.Revision); w.Code != http.StatusConflict || len(g.Config().Accounts) != 2 {
		t.Fatalf("stale review overwrote active config: status=%d body=%s", w.Code, w.Body.String())
	}
	impactResponse = adminControlRequest(g, http.MethodGet, "/accounts/old/impact", "", "")
	if err := json.Unmarshal(impactResponse.Body.Bytes(), &impact); err != nil {
		t.Fatal(err)
	}
	if w := adminControlRequest(g, http.MethodDelete, "/accounts/old", "", impact.Revision); w.Code != http.StatusOK {
		t.Fatalf("reviewed removal failed: %d %s", w.Code, w.Body.String())
	}
	if w := call(); w.Code != http.StatusOK || secondCalls.Load() != 1 || firstCalls.Load() != 1 {
		t.Fatalf("new call did not keep its alias on the replacement: status=%d calls=%d/%d body=%s", w.Code, firstCalls.Load(), secondCalls.Load(), w.Body.String())
	}
	if w := adminControlRequest(g, http.MethodDelete, "/client-keys/"+key.ID, "", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"in_flight":"continue"`) {
		t.Fatalf("revocation contract=%d %s", w.Code, w.Body.String())
	}
	if w := call(); w.Code != http.StatusUnauthorized || secondCalls.Load() != 1 || firstCalls.Load() != 1 {
		t.Fatalf("revoked key reached upstream: status=%d calls=%d/%d", w.Code, firstCalls.Load(), secondCalls.Load())
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case w := <-done:
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "chat-test") {
			t.Fatalf("admitted call did not finish under its original agreement: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("account exit or key revocation stranded an admitted call")
	}
}

func TestExplicitAliasRemovalCannotExposeUnapprovedImplicitSources(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = w.Write([]byte(completedChat)) }))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{
		{ID: "old", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1},
		{ID: "unapproved", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1},
	}, map[string]config.Route{"m": {Targets: []config.RouteTarget{{Account: "old", Model: "m"}}}})
	w := adminControlRequest(g, http.MethodGet, "/accounts/old/impact", "", "")
	var impact accountRemovalImpact
	if err := json.Unmarshal(w.Body.Bytes(), &impact); err != nil || impact.CanApply || !slices.Contains(impact.ImplicitConflicts, "m") {
		t.Fatalf("implicit model exposure was not identified: impact=%+v err=%v", impact, err)
	}
	for _, change := range []struct{ method, path, body string }{{http.MethodDelete, "/accounts/old", ""}, {http.MethodPut, "/routes", `{}`}} {
		w := adminControlRequest(g, change.method, change.path, change.body, impact.Revision)
		if w.Code != http.StatusBadRequest || len(g.Config().Accounts) != 2 || len(g.Config().Routes) != 1 || calls.Load() != 0 {
			t.Fatalf("invalid change was applied or called upstream: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
		}
	}
	for _, modelScope := range [][]string{nil, {"*"}} {
		account := g.Config().Accounts[1]
		account.Models = modelScope
		if err := g.UpsertAccount(account); err != nil {
			t.Fatal(err)
		}
		if w := adminControlRequest(g, http.MethodDelete, "/accounts/old", "", runtimeConfigRevision(g.state.Load())); w.Code != http.StatusBadRequest || calls.Load() != 0 {
			t.Fatalf("unknown/wildcard scope escaped the alias boundary: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestConfigRevisionTracksFrozenEnvironmentSources(t *testing.T) {
	t.Setenv("ROUND2_SOURCE_KEY", "original-fixture")
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: "https://example.invalid/v1", APIKeyEnv: "ROUND2_SOURCE_KEY", Models: []string{"m"}, Enabled: true}}, nil)
	original := runtimeConfigRevision(g.state.Load())
	t.Setenv("ROUND2_SOURCE_KEY", "replacement-fixture")
	if runtimeConfigRevision(g.state.Load()) != original {
		t.Fatal("revision followed live environment instead of the frozen runtime")
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	if runtimeConfigRevision(g.state.Load()) == original {
		t.Fatal("credential reload did not invalidate the reviewed source identity")
	}
}

func TestConfigPreconditionsCoverAccountRouteAndImportWrites(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	stale := runtimeConfigRevision(g.state.Load())
	if err := g.UpsertAccount(config.Account{ID: "new", Type: "openai", BaseURL: "https://example.invalid/v1", APIKey: "secret", Models: []string{"m"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ method, path, body string }{
		{http.MethodPut, "/accounts", `{"id":"other","type":"openai","base_url":"https://example.invalid/v1","api_key":"secret","models":["m"],"enabled":true}`},
		{http.MethodPut, "/routes", `{"m":{"accounts":["new"]}}`},
		{http.MethodPost, "/accounts/import", `{"data":{"type":"lite2api-data","version":1,"accounts":[{"id":"imported","type":"openai","base_url":"https://example.invalid/v1","api_key":"secret","models":["m"]}]},"dry_run":false}`},
	} {
		if w := adminControlRequest(g, change.method, change.path, change.body, stale); w.Code != http.StatusConflict || len(g.Config().Accounts) != 1 || len(g.Config().Routes) != 0 {
			t.Fatalf("stale %s %s changed active config: %d %s", change.method, change.path, w.Code, w.Body.String())
		}
	}
}

func TestCredentialPoliciesMatchAllRetainedHeadersAndLimits(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = w.Write([]byte(completedChat)) }))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	_, limited, err := g.clientKeys.Create(ClientKeyCreate{Name: "limited", RPM: 1, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, expired, err := g.clientKeys.Create(ClientKeyCreate{Name: "expired", ExpiresAt: "2000-01-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	_, disabledSecret, err := g.clientKeys.Create(ClientKeyCreate{Name: "disabled", Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	call := func(header, value string) int {
		r := gatewayRequest(`{"model":"m","messages":[{"role":"user","content":"ping"}]}`)
		r.Header.Del("Authorization")
		r.Header.Set(header, value)
		w := httptest.NewRecorder()
		g.ServeGateway(w, r)
		return w.Code
	}
	if status := call("Authorization", "Bearer "+limited); status != 200 {
		t.Fatalf("allowed=%d", status)
	}
	if status := call("X-Api-Key", limited); status != 429 || calls.Load() != 1 {
		t.Fatalf("alternate header bypassed RPM: status=%d calls=%d", status, calls.Load())
	}
	for _, secret := range []string{expired, disabledSecret, "admin-secret"} {
		for _, header := range []string{"Authorization", "X-Api-Key"} {
			value := secret
			if header == "Authorization" {
				value = "Bearer " + secret
			}
			if status := call(header, value); status != 401 || calls.Load() != 1 {
				t.Fatalf("rejected key called upstream: status=%d calls=%d", status, calls.Load())
			}
		}
	}
	r := gatewayRequest(`{"model":"m"}`)
	r.Header.Set("Authorization", "Bearer "+expired)
	r.Header.Set("X-Api-Key", "gateway-secret")
	rejected := httptest.NewRecorder()
	g.ServeGateway(rejected, r)
	if rejected.Code != http.StatusUnauthorized || calls.Load() != 1 {
		t.Fatal("dual headers bypassed the documented Bearer precedence")
	}
	for _, header := range []string{"Authorization", "X-Api-Key"} {
		value := "gateway-secret"
		if header == "Authorization" {
			value = "Bearer " + value
		}
		if status := call(header, value); status != 200 {
			t.Fatalf("legacy %s=%d", header, status)
		}
	}
	w := adminControlRequest(g, http.MethodGet, "/client-keys", "", "")
	var snapshot struct {
		Policy struct {
			Legacy struct {
				Count  int  `json:"count"`
				PerKey bool `json:"per_key_limits"`
			} `json:"legacy"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil || snapshot.Policy.Legacy.Count != 1 || snapshot.Policy.Legacy.PerKey {
		t.Fatalf("legacy policy was not explicit: %s err=%v", w.Body.String(), err)
	}
}
