package gateway

import (
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
		case "/v0/management/auth-files":
			writeJSON(w, http.StatusOK, map[string]any{"files": []map[string]any{{
				"id": "codex-user@example.com.json", "auth_index": "safe-index", "provider": "codex",
				"label": "user@example.com", "account_type": "plus", "status": "active", "success": 3, "failed": 1,
				"quota_windows": []map[string]any{{"kind": "five_hour", "used_percentage": 42.5, "observed_at": "2026-08-16T01:02:03Z", "source": "provider_response"}},
			}}})
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer "+serviceKey {
				t.Errorf("missing service authorization")
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]string{{"id": "gpt-test"}, {"id": "claude-test"}}})
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
			found = account.Enabled && len(account.Models) == 2 && account.APIKeyEnv == "CLIPROXYAPI_KEY"
		}
	}
	if !found {
		t.Fatal("OAuth pool account was not created and loaded")
	}

	accounts := adminOAuthRequest(http.MethodGet, "/admin/api/oauth/accounts", "", cookie, csrf)
	accountsW := httptest.NewRecorder()
	g.ServeAdminAPI(accountsW, accounts)
	if accountsW.Code != http.StatusOK || !strings.Contains(accountsW.Body.String(), `"identity":"us***@example.com"`) || !strings.Contains(accountsW.Body.String(), `"plan":"plus"`) || !strings.Contains(accountsW.Body.String(), `"used_percentage":42.5`) || strings.Contains(accountsW.Body.String(), `user@example.com`) {
		t.Fatalf("accounts status=%d body=%s", accountsW.Code, accountsW.Body.String())
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
