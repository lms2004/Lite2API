package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAdminOAuthAuthorizationFlowAddsPool(t *testing.T) {
	const managementKey = "management-test-secret"
	const serviceKey = "service-test-secret"
	var callbackReceived atomic.Bool
	var statusPatched atomic.Bool
	var refreshRequested atomic.Bool
	var priorityPatched atomic.Bool
	var routingUpdated atomic.Bool
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/codex-auth-url":
			if r.Header.Get("Authorization") != "Bearer "+managementKey {
				t.Errorf("missing management authorization")
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "url": "https://auth.example.test/authorize", "state": "state-123"})
		case "/v0/management/oauth-callback":
			var payload oauthCallbackInput
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Provider != "codex" || payload.State != "state-123" || !strings.Contains(payload.RedirectURL, "code=test-code") {
				t.Errorf("unexpected callback: %+v", payload)
			}
			callbackReceived.Store(true)
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		case "/v0/management/get-auth-status":
			status := "wait"
			if callbackReceived.Load() {
				status = "ok"
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": status})
		case "/v0/management/auth-files/status":
			var payload struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Name != "codex-user@example.com.json" || !payload.Disabled {
				t.Errorf("unexpected auth status patch: %+v", payload)
			}
			statusPatched.Store(true)
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "disabled": true})
		case "/v0/management/auth-files/refresh":
			var payload struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Name != "codex-user@example.com.json" {
				t.Errorf("unexpected auth refresh: %+v", payload)
			}
			refreshRequested.Store(true)
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "refreshed": 1, "skipped": 0, "files": []map[string]any{{"name": payload.Name}}})
		case "/v0/management/auth-files/fields":
			var payload struct {
				Name     string `json:"name"`
				Priority int    `json:"priority"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if r.Method != http.MethodPatch || payload.Name != "codex-user@example.com.json" || payload.Priority != 80 {
				t.Errorf("unexpected auth priority patch: method=%s payload=%+v", r.Method, payload)
			}
			priorityPatched.Store(true)
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		case "/v0/management/routing/strategy":
			switch r.Method {
			case http.MethodGet:
				writeJSON(w, http.StatusOK, map[string]any{"strategy": "round-robin"})
			case http.MethodPut:
				var payload struct {
					Value string `json:"value"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload.Value != "fill-first" {
					t.Errorf("unexpected OAuth routing strategy: %+v", payload)
				}
				routingUpdated.Store(true)
				writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		case "/v0/management/auth-files":
			writeJSON(w, http.StatusOK, map[string]any{"files": []map[string]any{{
				"id": "codex-user@example.com.json", "auth_index": "safe-index", "provider": "codex",
				"label": "user@example.com", "account_type": "plus", "priority": 20, "status": "active", "success": 3, "failed": 1,
				"recent_requests": []map[string]any{{"time": "00:00-00:10", "success": 2, "failed": 1}},
				"quota_windows":   []map[string]any{{"kind": "five_hour", "used_percentage": 42.5, "observed_at": "2026-08-16T01:02:03Z", "source": "provider_response"}},
				"prompt_usage":    map[string]any{"provider": "codex", "model": "gpt-5", "input_tokens": 128, "cached_tokens": 64, "output_tokens": 24, "reasoning_tokens": 8, "total_tokens": 160, "observed_at": "2026-08-16T01:02:03Z", "source": "provider_usage"},
			}}})
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer "+serviceKey {
				t.Errorf("missing service authorization")
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]string{
				{"id": "gpt-test"}, {"id": "claude-test"}, {"id": "gpt-5.6-sol"}, {"id": "gpt-5.6-luna"}, {"id": "fast"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer adapter.Close()

	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", managementKey)
	t.Setenv("CLIPROXYAPI_KEY", serviceKey)
	g := newTestGateway(t, nil, nil)
	cookie, csrf := loginAdminForTest(t, g)

	start := adminOAuthRequest(http.MethodPost, "/admin/api/oauth/start", `{"provider":"codex"}`, cookie, csrf)
	startW := httptest.NewRecorder()
	g.ServeAdminAPI(startW, start)
	if startW.Code != http.StatusOK || !strings.Contains(startW.Body.String(), `"callback_required":true`) || !strings.Contains(startW.Body.String(), `"state":"state-123"`) {
		t.Fatalf("start status=%d body=%s", startW.Code, startW.Body.String())
	}

	callback := adminOAuthRequest(http.MethodPost, "/admin/api/oauth/callback", `{"provider":"codex","state":"state-123","redirect_url":"http://localhost:1455/auth/callback?code=test-code&state=state-123"}`, cookie, csrf)
	callbackW := httptest.NewRecorder()
	g.ServeAdminAPI(callbackW, callback)
	if callbackW.Code != http.StatusOK || !callbackReceived.Load() {
		t.Fatalf("callback status=%d body=%s", callbackW.Code, callbackW.Body.String())
	}

	status := adminOAuthRequest(http.MethodPost, "/admin/api/oauth/status", `{"provider":"codex","state":"state-123"}`, cookie, csrf)
	statusW := httptest.NewRecorder()
	g.ServeAdminAPI(statusW, status)
	if statusW.Code != http.StatusOK || !strings.Contains(statusW.Body.String(), `"pool_ready":true`) || !strings.Contains(statusW.Body.String(), `"credential_count":1`) {
		t.Fatalf("status=%d body=%s", statusW.Code, statusW.Body.String())
	}
	var found bool
	for _, account := range g.Config().Accounts {
		if account.ID == "cliproxy-oauth" {
			hasSolCapability := false
			for _, capability := range account.Capabilities {
				if capability.Model == "sol" && capability.UpstreamModel == "gpt-5.6-sol" && containsModel(capability.ReasoningEfforts, "max") {
					hasSolCapability = true
					break
				}
			}
			found = account.Enabled && len(account.Models) == 5 && account.APIKeyEnv == "CLIPROXYAPI_KEY" && hasSolCapability
		}
	}
	if !found {
		t.Fatal("OAuth pool account was not created and loaded")
	}

	accounts := adminOAuthRequest(http.MethodGet, "/admin/api/oauth/accounts", "", cookie, csrf)
	accountsW := httptest.NewRecorder()
	g.ServeAdminAPI(accountsW, accounts)
	if accountsW.Code != http.StatusOK || !strings.Contains(accountsW.Body.String(), `"identity":"us***@example.com"`) || !strings.Contains(accountsW.Body.String(), `"plan":"plus"`) || !strings.Contains(accountsW.Body.String(), `"priority":20`) || !strings.Contains(accountsW.Body.String(), `"recent_success":2`) || !strings.Contains(accountsW.Body.String(), `"recent_failed":1`) || !strings.Contains(accountsW.Body.String(), `"used_percentage":42.5`) || !strings.Contains(accountsW.Body.String(), `"input_tokens":128`) || !strings.Contains(accountsW.Body.String(), `"cached_tokens":64`) || !strings.Contains(accountsW.Body.String(), `"reasoning_tokens":8`) || strings.Contains(accountsW.Body.String(), `user@example.com`) {
		t.Fatalf("accounts status=%d body=%s", accountsW.Code, accountsW.Body.String())
	}

	refresh := adminOAuthRequest(http.MethodPost, "/admin/api/oauth/accounts/refresh", `{"id":"safe-index"}`, cookie, csrf)
	refreshW := httptest.NewRecorder()
	g.ServeAdminAPI(refreshW, refresh)
	if refreshW.Code != http.StatusOK || !refreshRequested.Load() || !strings.Contains(refreshW.Body.String(), `"refreshed":1`) {
		t.Fatalf("refresh status=%d body=%s requested=%v", refreshW.Code, refreshW.Body.String(), refreshRequested.Load())
	}

	toggle := adminOAuthRequest(http.MethodPost, "/admin/api/oauth/accounts/status", `{"id":"safe-index","disabled":true}`, cookie, csrf)
	toggleW := httptest.NewRecorder()
	g.ServeAdminAPI(toggleW, toggle)
	if toggleW.Code != http.StatusOK || !statusPatched.Load() {
		t.Fatalf("toggle status=%d body=%s patched=%v", toggleW.Code, toggleW.Body.String(), statusPatched.Load())
	}

	priority := adminOAuthRequest(http.MethodPost, "/admin/api/oauth/accounts/priority", `{"id":"safe-index","priority":80}`, cookie, csrf)
	priorityW := httptest.NewRecorder()
	g.ServeAdminAPI(priorityW, priority)
	if priorityW.Code != http.StatusOK || !priorityPatched.Load() || !strings.Contains(priorityW.Body.String(), `"priority":80`) {
		t.Fatalf("priority status=%d body=%s patched=%v", priorityW.Code, priorityW.Body.String(), priorityPatched.Load())
	}

	routing := adminOAuthRequest(http.MethodGet, "/admin/api/oauth/routing", "", cookie, csrf)
	routingW := httptest.NewRecorder()
	g.ServeAdminAPI(routingW, routing)
	if routingW.Code != http.StatusOK || !strings.Contains(routingW.Body.String(), `"strategy":"round-robin"`) || !strings.Contains(routingW.Body.String(), `"automatic_failover":true`) {
		t.Fatalf("routing status=%d body=%s", routingW.Code, routingW.Body.String())
	}

	routingUpdate := adminOAuthRequest(http.MethodPut, "/admin/api/oauth/routing", `{"strategy":"fill_first"}`, cookie, csrf)
	routingUpdateW := httptest.NewRecorder()
	g.ServeAdminAPI(routingUpdateW, routingUpdate)
	if routingUpdateW.Code != http.StatusOK || !routingUpdated.Load() || !strings.Contains(routingUpdateW.Body.String(), `"strategy":"fill-first"`) {
		t.Fatalf("routing update status=%d body=%s updated=%v", routingUpdateW.Code, routingUpdateW.Body.String(), routingUpdated.Load())
	}

	invalidPriority := adminOAuthRequest(http.MethodPost, "/admin/api/oauth/accounts/priority", `{"id":"safe-index","priority":-1}`, cookie, csrf)
	invalidPriorityW := httptest.NewRecorder()
	g.ServeAdminAPI(invalidPriorityW, invalidPriority)
	if invalidPriorityW.Code != http.StatusBadRequest {
		t.Fatalf("invalid priority status=%d body=%s", invalidPriorityW.Code, invalidPriorityW.Body.String())
	}

	invalidRouting := adminOAuthRequest(http.MethodPut, "/admin/api/oauth/routing", `{}`, cookie, csrf)
	invalidRoutingW := httptest.NewRecorder()
	g.ServeAdminAPI(invalidRoutingW, invalidRouting)
	if invalidRoutingW.Code != http.StatusBadRequest {
		t.Fatalf("missing routing strategy status=%d body=%s", invalidRoutingW.Code, invalidRoutingW.Body.String())
	}
}

func TestAdminOAuthAccountDeleteRemovesAuthFile(t *testing.T) {
	const managementKey = "management-delete-secret"
	var deleted atomic.Bool
	var deletedName atomic.Value
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+managementKey {
			t.Errorf("missing management authorization")
		}
		if r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"files": []map[string]any{{
				"id": "codex-user@example.com.json", "name": "codex-user@example.com.json", "auth_index": "safe-index", "provider": "codex",
			}}})
		case http.MethodDelete:
			deletedName.Store(r.URL.Query().Get("name"))
			deleted.Store(true)
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer adapter.Close()

	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", managementKey)
	g := newTestGateway(t, nil, nil)
	cookie, csrf := loginAdminForTest(t, g)
	request := adminOAuthRequest(http.MethodDelete, "/admin/api/oauth/accounts", `{"id":"safe-index"}`, cookie, csrf)
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, request)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("delete status=%d body=%s", w.Code, w.Body.String())
	}
	if !deleted.Load() {
		t.Fatal("OAuth adapter did not receive the delete request")
	}
	if got := deletedName.Load(); got != "codex-user@example.com.json" {
		t.Fatalf("deleted auth file=%v", got)
	}
}

func TestMaskCredentialIdentity(t *testing.T) {
	for input, expected := range map[string]string{
		"a@example.com":     "a***@example.com",
		"alice@example.com": "al***@example.com",
		"very-secret-token": "ve***en",
		"":                  "已保存凭据",
	} {
		if actual := maskCredentialIdentity(input); actual != expected {
			t.Errorf("maskCredentialIdentity(%q)=%q want %q", input, actual, expected)
		}
	}
}

func TestNormalizeOAuthProvider(t *testing.T) {
	for input, expected := range map[string]string{
		"claude": "anthropic", "gemini-cli": "gemini", "openai": "codex", "antigravity": "antigravity",
	} {
		if actual := normalizeOAuthProvider(input); actual != expected {
			t.Errorf("normalizeOAuthProvider(%q)=%q want %q", input, actual, expected)
		}
	}
}

func TestNormalizeOAuthRoutingStrategy(t *testing.T) {
	for input, expected := range map[string]string{
		"": "round-robin", "round_robin": "round-robin", "rr": "round-robin", "fill_first": "fill-first", "ff": "fill-first",
	} {
		actual, ok := normalizeOAuthRoutingStrategy(input)
		if !ok || actual != expected {
			t.Errorf("normalizeOAuthRoutingStrategy(%q)=(%q,%v), want (%q,true)", input, actual, ok, expected)
		}
	}
	if _, ok := normalizeOAuthRoutingStrategy("random"); ok {
		t.Fatal("unsupported OAuth routing strategy was accepted")
	}
}

func TestShortCredentialIDIsStableAndRedacted(t *testing.T) {
	first := shortCredentialID("codex-user@example.com.json")
	if first == "" || len(first) != 16 || first != shortCredentialID("codex-user@example.com.json") {
		t.Fatalf("unexpected public credential id %q", first)
	}
	if strings.Contains(first, "user") || first == shortCredentialID("another@example.com.json") {
		t.Fatalf("credential id was not safely pseudonymized: %q", first)
	}
}

func TestOAuthCredentialPriorityOrderingAndRuntimeParentResolution(t *testing.T) {
	const managementKey = "management-order-secret"
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"files": []map[string]any{
			{"id": "low.json", "auth_index": "low", "provider": "codex", "status": "active", "priority": 10},
			{"id": "high-disabled.json", "auth_index": "high-disabled", "provider": "codex", "status": "active", "disabled": true, "priority": 30},
			{"id": "high-ready.json", "auth_index": "high-ready", "provider": "codex", "status": "active", "priority": 30},
			{"id": "virtual-project", "auth_index": "virtual", "provider": "gemini-cli", "status": "active", "runtime_only": true, "path": "/var/lib/cliproxyapi/auths/gemini-parent.json", "priority": 40},
		}})
	}))
	defer adapter.Close()
	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", managementKey)

	credentials, err := listOAuthCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 4 {
		t.Fatalf("credential count=%d", len(credentials))
	}
	if credentials[0].ID != "high-ready" || credentials[1].ID != "high-disabled" || credentials[2].ID != "low" || credentials[3].Provider != "gemini" {
		t.Fatalf("codex priority/readiness order=%+v", credentials)
	}
	if got := resolveOAuthAccountName(context.Background(), "virtual"); got != "gemini-parent.json" {
		t.Fatalf("runtime parent=%q", got)
	}
}

func TestOAuthAdapterRejectsUnsupportedProviderAndNonLoopbackURL(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	cookie, csrf := loginAdminForTest(t, g)
	request := adminOAuthRequest(http.MethodPost, "/admin/api/oauth/start", `{"provider":"unknown"}`, cookie, csrf)
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, request)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unsupported provider status=%d", w.Code)
	}

	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", "http://192.0.2.10:45682")
	if _, err := oauthAdapterBaseURL(); err == nil {
		t.Fatal("non-loopback OAuth adapter URL was accepted")
	}
}

func loginAdminForTest(t *testing.T, g *Gateway) (*http.Cookie, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/api/login", strings.NewReader(`{"token":"admin-secret"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, req)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 1 {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return w.Result().Cookies()[0], result.CSRF
}

func adminOAuthRequest(method, path, body string, cookie *http.Cookie, csrf string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(cookie)
	return req
}
