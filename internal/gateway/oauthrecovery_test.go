package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func TestOAuth401RefreshesSelectedCredentialAndRetriesOnce(t *testing.T) {
	const credential = "0123456789abcdef"
	var calls, refreshes atomic.Int64
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set(credentialSelectedHeader, credential)
			if calls.Add(1) == 1 {
				http.Error(w, "access token expired: sensitive-detail", 401)
				return
			}
			writeJSON(w, 200, map[string]any{"choices": []any{map[string]string{"text": "ok"}}})
		case "/v0/management/auth-files":
			if r.Header.Get("Authorization") != "Bearer management-secret" {
				t.Error("missing management authentication")
			}
			writeJSON(w, 200, map[string]any{"files": []any{map[string]any{"auth_index": credential, "name": "codex-private.json"}}})
		case "/v0/management/auth-files/refresh":
			var input map[string]string
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input["name"] != "codex-private.json" {
				t.Errorf("wrong credential refreshed: %v", input)
			}
			refreshes.Add(1)
			writeJSON(w, 200, oauthRefreshResult{Refreshed: 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer adapter.Close()
	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "management-secret")
	g := newTestGateway(t, []config.Account{{ID: "pool", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: adapter.URL + "/v1", APIKey: "service-secret", Models: []string{"m"}, Enabled: true, Concurrency: 2, Weight: 1}}, nil)
	w := httptest.NewRecorder()
	g.ServeGateway(w, gatewayRequest(`{"model":"m","messages":[]}`))
	if w.Code != 200 || calls.Load() != 2 || refreshes.Load() != 1 {
		t.Fatalf("status=%d calls=%d refreshes=%d body=%s", w.Code, calls.Load(), refreshes.Load(), w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sensitive") || w.Header().Get(credentialSelectedHeader) != "" {
		t.Fatal("internal credential information leaked to client")
	}
	if g.Stats().Active != 0 || g.bodyBytes.Load() != 0 {
		t.Fatal("recovery leaked admission/body leases")
	}
}

func TestOAuthRecoveryCoalescesConcurrentRefreshAndHonorsCancellation(t *testing.T) {
	var coordinator oauthRecoveryCoordinator
	var calls atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	refresh := func(context.Context) bool {
		calls.Add(1)
		close(started)
		<-release
		return true
	}
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if !coordinator.refresh(context.Background(), "credential", refresh) {
				t.Error("shared refresh failed")
			}
		}()
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if coordinator.refresh(ctx, "credential", refresh) {
		t.Fatal("canceled waiter received refresh success")
	}
	close(release)
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls=%d, want one", calls.Load())
	}
}

func TestOAuthRecoveryNeverRefreshesOrReplaysAmbiguousFailures(t *testing.T) {
	for _, status := range []int{401, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int64
			adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set(credentialSelectedHeader, "0123456789abcdef")
				http.Error(w, "private-upstream-detail", status)
			}))
			defer adapter.Close()
			// An adapter ID alone cannot authorize refreshing a different origin.
			t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", "http://127.0.0.1:1")
			t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "secret")
			g := newTestGateway(t, []config.Account{{ID: "pool", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: adapter.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Concurrency: 1, Weight: 1}}, nil)
			w := httptest.NewRecorder()
			g.ServeGateway(w, gatewayRequest(`{"model":"m"}`))
			code := "uncertain_submission"
			if status == 401 {
				code = "upstream_auth_failed"
			}
			if w.Code != 502 || !strings.Contains(w.Body.String(), code) || requests.Load() != 1 || strings.Contains(w.Body.String(), "private-upstream") {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, requests.Load(), w.Body.String())
			}
		})
	}
}

func TestUpstreamAuthFailureUsesProviderProtocolAndPreservesClient401(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("WWW-Authenticate", "Bearer private-detail")
				http.Error(w, "access_token=secret", 403)
			}))
			defer upstream.Close()
			g := newTestGateway(t, []config.Account{{ID: "upstream", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Operations: []string{config.OperationOpenAIChat, config.OperationAnthropic}, Enabled: true, Weight: 1}}, nil)
			r := gatewayRequest(`{"model":"m"}`)
			r.URL.Path = path
			w := httptest.NewRecorder()
			g.ServeGateway(w, r)
			if w.Code != 502 || strings.Contains(w.Body.String(), "secret") || w.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("invalid upstream failure response: %d %s", w.Code, w.Body.String())
			}
			if path == "/v1/messages" && !strings.Contains(w.Body.String(), `"type":"api_error"`) {
				t.Fatal("missing Anthropic error envelope")
			}
			r = gatewayRequest(`{"model":"m"}`)
			r.Header.Set("Authorization", "Bearer invalid-client-key")
			w = httptest.NewRecorder()
			g.ServeGateway(w, r)
			if w.Code != 401 {
				t.Fatal("client authentication failure no longer returns 401")
			}
		})
	}
}

func TestOAuthAccountHealthReasonsAndRefreshRedaction(t *testing.T) {
	for _, item := range []struct{ status, message, retry, code string }{
		{"unavailable", `OAuth access token has expired. Re-authenticate to continue.`, "", "reauth_required"},
		{"error", `invalid_grant refresh_token=secret`, "", "reauth_required"},
		{"disabled", "invalid_grant", "", "disabled"},
		{"unavailable", "", time.Now().Add(time.Hour).Format(time.RFC3339), "cooldown"},
		{"unavailable", "", time.Now().Add(-time.Hour).Format(time.RFC3339), "upstream_error"},
	} {
		code, message := oauthHealthReason(item.status, item.message, item.retry, false)
		if code != item.code || strings.Contains(message, "secret") {
			t.Errorf("status=%s code=%s message=%s", item.status, code, message)
		}
	}
	entries := sanitizedOAuthRefreshEntries([]oauthRefreshEntry{{Name: "private@example.com.json", Error: "invalid_grant refresh_token=secret"}}, true)
	data, _ := json.Marshal(entries)
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "private@") || !strings.Contains(string(data), "reauth_required") {
		t.Fatalf("unsafe refresh response: %s", data)
	}
	w := httptest.NewRecorder()
	writeOAuthAdapterError(w, errors.New("upstream failed: access_token=private-token"))
	if w.Code != 502 || strings.Contains(w.Body.String(), "private-token") {
		t.Fatalf("unsafe management error: %s", w.Body.String())
	}
}

func TestOAuthRefreshRequiresExplicitTarget(t *testing.T) {
	for _, body := range []string{`{}`, `{"id":"account","all":true}`} {
		w := httptest.NewRecorder()
		g := &Gateway{}
		g.serveOAuthAccountRefresh(w, httptest.NewRequest(http.MethodPost, "/admin/api/oauth/accounts/refresh", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_oauth_refresh_target") {
			t.Fatalf("ambiguous refresh target accepted: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestUnavailableOAuthPoolHasExplicitProtocolError(t *testing.T) {
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(credentialRetrySafeHeader, "true")
		http.Error(w, "private credential selection detail", http.StatusServiceUnavailable)
	}))
	defer adapter.Close()
	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		g := newTestGateway(t, []config.Account{{ID: "pool", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: adapter.URL + "/v1", APIKey: "test", Models: []string{"m"}, Operations: []string{config.OperationOpenAIChat, config.OperationAnthropic}, Enabled: true, Weight: 1}}, nil)
		r := gatewayRequest(`{"model":"m"}`)
		r.URL.Path = path
		w := httptest.NewRecorder()
		g.ServeGateway(w, r)
		code := "upstream_unavailable"
		if path == "/v1/messages" {
			code = "overloaded_error"
		}
		if w.Code != 503 || !strings.Contains(w.Body.String(), code) || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("path=%s status=%d body=%s", path, w.Code, w.Body.String())
		}
	}
}

func TestFailedOAuthRecoveryIsThrottled(t *testing.T) {
	var coordinator oauthRecoveryCoordinator
	calls := 0
	for i := 0; i < 10; i++ {
		if coordinator.refresh(context.Background(), "expired", func(context.Context) bool { calls++; return false }) {
			t.Fatal("failed refresh reported success")
		}
	}
	if calls != 1 {
		t.Fatalf("failed refresh called %d times within backoff", calls)
	}
}
