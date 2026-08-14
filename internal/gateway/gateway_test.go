package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func newTestGateway(t testing.TB, accounts []config.Account, routes map[string]config.Route) *Gateway {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.APIKeys = []string{"gateway-secret"}
	cfg.Server.AdminToken = "admin-secret"
	cfg.Server.AllowPrivateHTTPUpstream = true
	cfg.Server.QueueTimeout = config.Duration{}
	cfg.Accounts = accounts
	cfg.Routes = routes
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.NewStore(path).Save(cfg); err != nil {
		t.Fatal(err)
	}
	g, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func BenchmarkGatewayParallel(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
	}))
	defer upstream.Close()
	g := newTestGateway(b, []config.Account{{ID: "bench", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 0, Weight: 1, Enabled: true}}, nil)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			w := httptest.NewRecorder()
			g.ServeGateway(w, gatewayRequest(`{"model":"m","messages":[{"role":"user","content":"ping"}]}`))
			if w.Code != http.StatusOK {
				b.Fatalf("status=%d", w.Code)
			}
		}
	})
}

func gatewayRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer gateway-secret")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestBuildUpstreamURLCompatibilityRoots(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{name: "OpenAI v1 root", base: "https://api.example.com/v1", want: "https://api.example.com/v1/chat/completions?trace=1"},
		{name: "Gemini OpenAI root", base: "https://generativelanguage.googleapis.com/v1beta/openai", want: "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions?trace=1"},
		{name: "custom prefix keeps v1", base: "https://api.example.com/proxy", want: "https://api.example.com/proxy/v1/chat/completions?trace=1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := buildUpstreamURL(test.base, "/v1/chat/completions", "trace=1")
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("URL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestGatewayRewritesModelAndAuthenticates(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Errorf("auth=%q", got)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "real-model" {
			t.Errorf("model=%v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "main", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "upstream-secret", Models: []string{"alias"}, ModelMap: map[string]string{"alias": "real-model"}, Concurrency: 2, Weight: 1, Enabled: true}}, nil)
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"alias","messages":[]}`))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	bad := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alias"}`))
	badW := httptest.NewRecorder()
	g.ServeGateway(badW, bad)
	if badW.Code != 401 {
		t.Fatalf("unauthorized status=%d", badW.Code)
	}
}

func TestGatewayFailsOver(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { firstCalls.Add(1); http.Error(w, "busy", 503) }))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer second.Close()
	accounts := []config.Account{{ID: "first", Type: "openai", BaseURL: first.URL + "/v1", APIKey: "test", Models: []string{"m"}, Priority: 0, Concurrency: 1, Weight: 1, Enabled: true}, {ID: "second", Type: "openai", BaseURL: second.URL + "/v1", APIKey: "test", Models: []string{"m"}, Priority: 1, Concurrency: 1, Weight: 1, Enabled: true}}
	g := newTestGateway(t, accounts, map[string]config.Route{"m": {Accounts: []string{"first", "second"}, Strategy: "priority"}})
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"m","messages":[]}`))
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("calls=%d,%d", firstCalls.Load(), secondCalls.Load())
	}
	if g.Stats().Failovers != 1 {
		t.Fatalf("failovers=%d", g.Stats().Failovers)
	}
}

func TestAuthenticationFailureTripsCircuit(t *testing.T) {
	var firstCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) }))
	defer second.Close()
	accounts := []config.Account{{ID: "first", Type: "openai", BaseURL: first.URL + "/v1", APIKey: "bad", Models: []string{"m"}, Priority: 0, Concurrency: 1, Weight: 1, Enabled: true}, {ID: "second", Type: "openai", BaseURL: second.URL + "/v1", APIKey: "ok", Models: []string{"m"}, Priority: 1, Concurrency: 1, Weight: 1, Enabled: true}}
	g := newTestGateway(t, accounts, map[string]config.Route{"m": {Accounts: []string{"first", "second"}, Strategy: "priority"}})
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		g.ServeGateway(w, gatewayRequest(`{"model":"m"}`))
		if w.Code != 200 {
			t.Fatalf("request %d status=%d", i, w.Code)
		}
	}
	if got := firstCalls.Load(); got != 1 {
		t.Fatalf("circuit did not skip failed credential: calls=%d", got)
	}
}

func TestStreamingHoldsConcurrencySlot(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		close(started)
		<-release
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "stream", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true}}, nil)
	cfg := g.Config()
	cfg.Server.MaxInFlightRequests = 1
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.ServeGateway(httptest.NewRecorder(), gatewayRequest(`{"model":"m","stream":true}`))
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stream did not start")
	}
	if active := g.Accounts()[0].Active; active != 1 {
		t.Fatalf("active=%d, want 1 while stream is open", active)
	}
	updated := g.Config().Accounts[0]
	updated.Name = "updated while streaming"
	if err := g.UpsertAccount(updated); err != nil {
		t.Fatalf("hot reload: %v", err)
	}
	if active := g.Accounts()[0].Active; active != 1 {
		t.Fatalf("active=%d after hot reload, want shared active slot", active)
	}
	secondW := httptest.NewRecorder()
	g.ServeGateway(secondW, gatewayRequest(`{"model":"m"}`))
	if secondW.Code != http.StatusTooManyRequests {
		t.Fatalf("global limit status=%d, want 429", secondW.Code)
	}
	selection, err := g.state.Load().scheduler.Select(context.Background(), "m", "", nil, 0)
	if err != ErrNoCapacity || selection != nil {
		t.Fatalf("second selection=%v err=%v", selection, err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gateway did not finish")
	}
	if active := g.Accounts()[0].Active; active != 0 {
		t.Fatalf("active=%d after stream", active)
	}
}

func TestStreamIdleTimeoutReleasesSlot(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "idle", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Concurrency: 1, Weight: 1, Enabled: true}}, nil)
	cfg := g.Config()
	cfg.Server.StreamIdleTimeout = config.Duration{Duration: 30 * time.Millisecond}
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	started := time.Now()
	g.ServeGateway(w, gatewayRequest(`{"model":"m","stream":true}`))
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("idle timeout took %s", elapsed)
	}
	if active := g.Accounts()[0].Active; active != 0 {
		t.Fatalf("active=%d after idle timeout", active)
	}
	if recent := g.Stats().Recent; len(recent) == 0 || !strings.Contains(recent[0].Error, "stream idle") {
		t.Fatalf("idle timeout not recorded: %+v", recent)
	}
}

func TestModelsListsAliases(t *testing.T) {
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test", Models: []string{"direct"}, Enabled: true, Weight: 1}}, map[string]config.Route{"alias": {Accounts: []string{"a"}}})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer gateway-secret")
	w := httptest.NewRecorder()
	g.ServeGateway(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if body := w.Body.String(); body == "" || !strings.Contains(body, "alias") || !strings.Contains(body, "direct") {
		t.Fatalf("body=%s", body)
	}
}

func TestAdminAuthenticationAndRouteUpdate(t *testing.T) {
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "upstream-secret", Headers: map[string]string{"X-Private": "header-secret"}, Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	unauthorized := httptest.NewRecorder()
	unauthorizedReq := httptest.NewRequest(http.MethodGet, "/admin/api/state", nil)
	unauthorizedReq.RemoteAddr = "127.0.0.1:12345"
	g.ServeAdminAPI(unauthorized, unauthorizedReq)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("admin unauthorized=%d", unauthorized.Code)
	}
	body := `{"alias":{"accounts":["a"],"upstream_model":"m","strategy":"sticky"}}`
	req := httptest.NewRequest(http.MethodPut, "/admin/api/routes", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, req)
	if w.Code != 200 || g.Config().Routes["alias"].Strategy != "sticky" {
		t.Fatalf("route update status=%d config=%v", w.Code, g.Config().Routes)
	}
	stateReq := httptest.NewRequest(http.MethodGet, "/admin/api/state", nil)
	stateReq.RemoteAddr = "127.0.0.1:12345"
	stateReq.Header.Set("Authorization", "Bearer admin-secret")
	stateW := httptest.NewRecorder()
	g.ServeAdminAPI(stateW, stateReq)
	if strings.Contains(stateW.Body.String(), "upstream-secret") || strings.Contains(stateW.Body.String(), "header-secret") {
		t.Fatalf("admin response leaked a secret: %s", stateW.Body.String())
	}
}

func TestAdminAccountUpdatePreservesRedactedSecrets(t *testing.T) {
	g := newTestGateway(t, []config.Account{{
		ID: "a", Name: "Before", Type: "openai", BaseURL: "https://api.example.com/v1",
		APIKey: "upstream-secret", Headers: map[string]string{"X-Private": "header-secret"},
		Models: []string{"m"}, Enabled: true, Weight: 1,
	}}, nil)
	account := redactedConfig(g.Config()).Accounts[0]
	account.Name = "After"
	account.APIKey = ""
	if err := g.UpsertAccount(account); err != nil {
		t.Fatal(err)
	}
	updated := g.Config().Accounts[0]
	if updated.Name != "After" || updated.APIKey != "upstream-secret" || updated.Headers["X-Private"] != "header-secret" {
		t.Fatalf("updated account=%+v", updated)
	}
}

func TestRequestBodyLimit(t *testing.T) {
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	cfg := g.Config()
	cfg.Server.MaxBodyBytes = 16
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"m","padding":"this is too large"}`))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestInvalidUpdateDoesNotReplaceRunningConfig(t *testing.T) {
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, nil)
	invalid := g.Config().Accounts[0]
	invalid.BaseURL = "file:///etc/passwd"
	if err := g.UpsertAccount(invalid); err == nil {
		t.Fatal("expected invalid update to fail")
	}
	if got := g.Config().Accounts[0].BaseURL; got != "http://127.0.0.1:1/v1" {
		t.Fatalf("running config changed to %q", got)
	}
	if err := g.Reload(); err != nil {
		t.Fatalf("persisted config was corrupted: %v", err)
	}
}
