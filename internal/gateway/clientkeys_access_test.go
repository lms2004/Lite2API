package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestManagedClientPermissionsAffectCatalogCallsAndRevocation(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Admin-Token") != "" || r.Header.Get("X-CSRF-Token") != "" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Error("the upstream received client or management credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{
		ID: "a", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "upstream-secret",
		Models: []string{"allowed", "blocked"}, Enabled: true, Weight: 1,
	}}, nil)
	key, secret, err := g.clientKeys.Create(ClientKeyCreate{Name: "restricted", Models: []string{"allowed"}, RPM: 30, Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Api-Key", secret)
		r.Header.Set("X-Admin-Token", "management-canary")
		r.Header.Set("X-CSRF-Token", "csrf-canary")
		r.Header.Set("Cookie", "lite2api_admin_session=cookie-canary")
		w := httptest.NewRecorder()
		g.ServeGateway(w, r)
		return w
	}
	if w := request(http.MethodGet, "/v1/models", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"allowed"`) || strings.Contains(w.Body.String(), `"id":"blocked"`) {
		t.Fatalf("model catalog did not apply permissions: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, "/v1/chat/completions", `{"model":"blocked","messages":[{"role":"user","content":"ping"}]}`); w.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("forbidden model reached upstream: status=%d calls=%d", w.Code, calls.Load())
	}
	if w := request(http.MethodPost, "/v1/chat/completions", `{"model":"allowed","messages":[{"role":"user","content":"ping"}]}`); w.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("allowed call failed: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
	models := []string{"blocked"}
	if _, err := g.clientKeys.Update(key.ID, ClientKeyUpdate{Models: &models}); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodPost, "/v1/chat/completions", `{"model":"allowed"}`); w.Code != http.StatusForbidden || calls.Load() != 1 {
		t.Fatalf("changed permission did not affect later access: status=%d calls=%d", w.Code, calls.Load())
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodPost, "/v1/chat/completions", `{"model":"allowed"}`); w.Code != http.StatusForbidden || calls.Load() != 1 {
		t.Fatalf("configuration reload reverted key permissions: status=%d calls=%d", w.Code, calls.Load())
	}
	if err := g.clientKeys.Delete(key.ID); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/v1/models", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key accessed the catalog: status=%d", w.Code)
	}
	cfg := g.Config()
	cfg.Server.APIKeys = []string{"replacement-legacy-key"}
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"allowed"}`))
	if w.Code != http.StatusUnauthorized || calls.Load() != 1 {
		t.Fatalf("legacy key removal did not revoke access: status=%d calls=%d", w.Code, calls.Load())
	}
}
