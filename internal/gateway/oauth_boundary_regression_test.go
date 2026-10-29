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

func TestOAuthStatusRejectsUnknownProviderBeforeAdapter(t *testing.T) {
	var calls atomic.Int64
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"status": "wait"})
	}))
	defer adapter.Close()
	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "test-management")
	g := newTestGateway(t, nil, nil)
	cookie, csrf := loginAdminForTest(t, g)
	for _, provider := range []string{"unknown", "CLAUDE", "https://example.test"} {
		w := httptest.NewRecorder()
		g.ServeAdminAPI(w, adminOAuthRequest(http.MethodPost, "/admin/api/oauth/status", `{"provider":"`+provider+`","state":"valid-state"}`, cookie, csrf))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unsupported_oauth_provider") {
			t.Fatalf("unknown provider status=%d", w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid provider reached adapter")
	}
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, adminOAuthRequest(http.MethodPost, "/admin/api/oauth/status", `{"state":"valid-state"}`, cookie, csrf))
	if w.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("legacy omitted provider status=%d calls=%d", w.Code, calls.Load())
	}
}

func TestOAuthCredentialRemovalReconcilesPoolAndSubsequentCalls(t *testing.T) {
	for _, action := range []string{"delete", "disable"} {
		t.Run(action, func(t *testing.T) {
			var mu sync.Mutex
			active := map[string]bool{"a": true, "b": true}
			var invocations atomic.Int64
			adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/v0/management/auth-files/download":
					writeJSON(w, 200, map[string]any{"type": "codex", "access_token": "fixture-snapshot"})
				case "/v0/management/auth-files":
					if r.Method == http.MethodDelete {
						delete(active, strings.TrimSuffix(r.URL.Query().Get("name"), ".json"))
						writeJSON(w, 200, map[string]any{"status": "ok"})
						return
					}
					files := []map[string]any{}
					for name, enabled := range active {
						files = append(files, map[string]any{"name": name + ".json", "auth_index": name, "provider": "codex", "status": "active", "disabled": !enabled})
					}
					writeJSON(w, 200, map[string]any{"files": files})
				case "/v0/management/auth-files/status":
					var body struct {
						Name     string `json:"name"`
						Disabled bool   `json:"disabled"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					active[strings.TrimSuffix(body.Name, ".json")] = !body.Disabled
					writeJSON(w, 200, map[string]any{"status": "ok"})
				case "/v1/models":
					models := []map[string]string{}
					for name, enabled := range active {
						if enabled {
							models = append(models, map[string]string{"id": "model-" + name})
						}
					}
					writeJSON(w, 200, map[string]any{"data": models})
				case "/v1/chat/completions":
					invocations.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(completedChat))
				default:
					http.NotFound(w, r)
				}
			}))
			defer adapter.Close()
			t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
			t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "test-management")
			t.Setenv("CLIPROXYAPI_KEY", "test-service")
			pool := config.Account{ID: "cliproxy-oauth", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: adapter.URL + "/v1", APIKeyEnv: "CLIPROXYAPI_KEY", Enabled: true, Models: []string{"model-a", "model-b"}, ModelMap: map[string]string{"alias-a": "model-a"}, Capabilities: []config.ChannelCapability{{Model: "cap-a", UpstreamModel: "model-a", ReasoningEfforts: []string{"auto"}}}}
			g := newTestGateway(t, []config.Account{pool}, nil)
			cookie, csrf := loginAdminForTest(t, g)
			remove := func(id string) {
				t.Helper()
				method, path, body := http.MethodDelete, "/admin/api/oauth/accounts", `{"id":"`+id+`"}`
				if action == "disable" {
					method, path, body = http.MethodPost, "/admin/api/oauth/accounts/status", `{"id":"`+id+`","disabled":true}`
				}
				w := httptest.NewRecorder()
				g.ServeAdminAPI(w, adminOAuthRequest(method, path, body, cookie, csrf))
				if w.Code != 200 {
					t.Fatalf("credential mutation: %d %s", w.Code, w.Body.String())
				}
			}
			call := func(model string) int {
				w := httptest.NewRecorder()
				g.ServeGateway(w, gatewayRequest(`{"model":"`+model+`","messages":[{"role":"user","content":"ping"}]}`))
				return w.Code
			}
			remove("a")
			current := g.Config().Accounts[0]
			if !current.Enabled || len(current.Models) != 1 || current.Models[0] != "model-b" || len(current.ModelMap) != 0 || len(current.Capabilities) != 0 {
				t.Fatalf("removed source still advertised: %+v", current)
			}
			if code := call("model-a"); code == 200 {
				t.Fatal("removed model accepted")
			}
			if invocations.Load() != 0 {
				t.Fatal("removed model reached adapter")
			}
			if code := call("model-b"); code != 200 {
				t.Fatalf("remaining model status=%d", code)
			}
			remove("b")
			current = g.Config().Accounts[0]
			if current.Enabled || len(current.Models) != 0 {
				t.Fatalf("empty pool not disabled: %+v", current)
			}
			if code := call("model-b"); code == 200 || invocations.Load() != 1 {
				t.Fatalf("empty pool accepted request: status=%d calls=%d", code, invocations.Load())
			}
			disk, err := config.Load(g.configPath)
			if err != nil || disk.Accounts[0].Enabled {
				t.Fatalf("empty pool state not persisted: %v", err)
			}
		})
	}
}

func TestAccountImportOAuthOnlyChecksRevisionBeforeUpload(t *testing.T) {
	var calls atomic.Int64
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, 200, map[string]any{"status": "ok"})
	}))
	defer adapter.Close()
	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "test-management")
	g := newTestGateway(t, nil, nil)
	revision := runtimeConfigRevision(g.state.Load())
	if err := g.UpsertAccount(config.Account{ID: "newer", Type: "openai", BaseURL: "https://example.test/v1", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	result, err := g.ImportAccounts(context.Background(), AccountImportRequest{ExpectedRevision: revision, Data: newAccountImportData([]AccountImportItem{oauthBoundaryImport("new")})})
	if !errors.Is(err, ErrConfigRevisionConflict) || result.Applied || result.OAuthImported != 0 || calls.Load() != 0 {
		t.Fatalf("stale import result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func oauthBoundaryImport(name string) AccountImportItem {
	return AccountImportItem{Name: name, Platform: "codex", Type: "oauth", Credentials: map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-refresh"}}
}

func TestAccountImportRollsBackNewAndReplacedCredentialsOnCommitFailure(t *testing.T) {
	for _, failure := range []string{"commit", "cancel_after_upload", "cancel_during_discovery"} {
		for _, disabled := range []bool{false, true} {
			name := failure + "/enabled"
			if disabled {
				name = failure + "/disabled"
			}
			t.Run(name, func(t *testing.T) {
				testAccountImportRollback(t, failure, disabled)
			})
		}
	}
}

func testAccountImportRollback(t *testing.T, failure string, originalDisabled bool) {
	t.Helper()
	var mu sync.Mutex
	original := json.RawMessage(`{"type":"codex","access_token":"fixture-original","refresh_token":"fixture-old"}`)
	files := map[string]json.RawMessage{"codex-existing.json": original}
	disabled := map[string]bool{"codex-existing.json": originalDisabled}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var gateway *Gateway
	uploads := 0
	var statusRestored bool
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		name := r.URL.Query().Get("name")
		switch r.URL.Path {
		case "/v0/management/auth-files":
			switch r.Method {
			case http.MethodGet:
				rows := []map[string]any{}
				for name := range files {
					rows = append(rows, map[string]any{"name": name, "provider": "codex", "disabled": disabled[name]})
				}
				writeJSON(w, 200, map[string]any{"files": rows})
			case http.MethodPost:
				var raw json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
					t.Error(err)
				}
				files[name] = raw
				// Uploading replaces runtime state as well as the saved token.
				disabled[name] = false
				uploads++
				if uploads == 2 {
					switch failure {
					case "commit":
						gateway.closed.Store(true)
					case "cancel_after_upload":
						cancel()
					}
				}
				writeJSON(w, 200, map[string]any{"status": "ok"})
			case http.MethodDelete:
				delete(files, name)
				delete(disabled, name)
				writeJSON(w, 200, map[string]any{"status": "ok"})
			}
		case "/v0/management/auth-files/download":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(files[name])
		case "/v0/management/auth-files/status":
			var change struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&change); err != nil {
				t.Error(err)
			}
			disabled[change.Name] = change.Disabled
			statusRestored = true
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		case "/v1/models":
			if failure == "cancel_during_discovery" {
				cancel()
			}
			writeJSON(w, 200, map[string]any{"data": []map[string]string{{"id": "gpt-fixture"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer adapter.Close()
	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "test-management")
	t.Setenv("CLIPROXYAPI_KEY", "test-service")
	gateway = newTestGateway(t, nil, nil)
	result, err := gateway.ImportAccounts(ctx, AccountImportRequest{ExpectedRevision: runtimeConfigRevision(gateway.state.Load()), Data: newAccountImportData([]AccountImportItem{oauthBoundaryImport("existing"), oauthBoundaryImport("new")})})
	if err == nil || result.Applied || result.OAuthImported != 0 {
		t.Fatalf("failed commit reported success: %+v err=%v", result, err)
	}
	if failure != "commit" && !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled import lost its error: %v", err)
	}
	if strings.Contains(err.Error(), "rollback incomplete") {
		t.Fatalf("credential rollback failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	var got, want map[string]any
	_ = json.Unmarshal(files["codex-existing.json"], &got)
	_ = json.Unmarshal(original, &want)
	if len(files) != 1 || got["access_token"] != want["access_token"] || got["refresh_token"] != want["refresh_token"] {
		t.Fatal("credential rollback did not restore existing file and remove new file")
	}
	if len(gateway.Config().Accounts) != 0 {
		t.Fatal("failed candidate became live")
	}
	if !statusRestored || disabled["codex-existing.json"] != originalDisabled {
		t.Fatal("existing credential runtime status was not restored")
	}
}

func TestAccountImportSerializesUploadWithConfigChanges(t *testing.T) {
	uploaded, release := make(chan struct{}), make(chan struct{})
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/auth-files":
			if r.Method == http.MethodPost {
				close(uploaded)
				<-release
				writeJSON(w, 200, map[string]any{"status": "ok"})
				return
			}
			writeJSON(w, 200, map[string]any{"files": []any{}})
		case "/v1/models":
			writeJSON(w, 200, map[string]any{"data": []map[string]string{{"id": "gpt-fixture"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer adapter.Close()
	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "test-management")
	t.Setenv("CLIPROXYAPI_KEY", "test-service")
	g := newTestGateway(t, nil, nil)
	imported := make(chan error, 1)
	go func() {
		_, err := g.ImportAccounts(context.Background(), AccountImportRequest{ExpectedRevision: runtimeConfigRevision(g.state.Load()), Data: newAccountImportData([]AccountImportItem{oauthBoundaryImport("new")})})
		imported <- err
	}()
	select {
	case <-uploaded:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not start")
	}
	changed := make(chan error, 1)
	go func() {
		changed <- g.UpsertAccount(config.Account{ID: "parallel", Type: "openai", BaseURL: "https://example.test/v1", Enabled: false})
	}()
	select {
	case err := <-changed:
		close(release)
		t.Fatalf("config update passed active import: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-imported:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("import stuck")
	}
	select {
	case err := <-changed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("config update stuck")
	}
	if len(g.Config().Accounts) != 2 {
		t.Fatal("import or subsequent config update was lost")
	}
}

func TestOAuthCredentialRemovalPreservesScopedRoutesAndOtherProviders(t *testing.T) {
	for _, action := range []string{"delete", "disable"} {
		for _, routeKind := range []string{"logical", "legacy"} {
			t.Run(action+"/"+routeKind, func(t *testing.T) {
				var mu sync.Mutex
				active := map[string]bool{"a": true, "b": true, "claude": true, "anti": true}
				providers := map[string]string{"a": "codex", "b": "codex", "claude": "claude", "anti": "antigravity"}
				models := map[string]string{"a": "gpt-5.6-sol", "b": "gpt-5.6-luna", "claude": "claude-code/claude-fixture", "anti": "antigravity/claude-fixture"}
				var invocations atomic.Int64
				adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					switch r.URL.Path {
					case "/v0/management/auth-files/download":
						writeJSON(w, 200, map[string]any{"type": "codex", "access_token": "fixture-snapshot"})
					case "/v0/management/auth-files":
						if r.Method == http.MethodDelete {
							delete(active, strings.TrimSuffix(r.URL.Query().Get("name"), ".json"))
							writeJSON(w, 200, map[string]any{"status": "ok"})
							return
						}
						files := []map[string]any{}
						for name, enabled := range active {
							files = append(files, map[string]any{"name": name + ".json", "auth_index": name, "provider": providers[name], "status": "active", "disabled": !enabled})
						}
						writeJSON(w, 200, map[string]any{"files": files})
					case "/v0/management/auth-files/status":
						var body struct {
							Name     string `json:"name"`
							Disabled bool   `json:"disabled"`
						}
						_ = json.NewDecoder(r.Body).Decode(&body)
						active[strings.TrimSuffix(body.Name, ".json")] = !body.Disabled
						writeJSON(w, 200, map[string]any{"status": "ok"})
					case "/v1/models":
						catalog := []map[string]string{}
						for name, enabled := range active {
							if enabled {
								catalog = append(catalog, map[string]string{"id": models[name]})
							}
						}
						writeJSON(w, 200, map[string]any{"data": catalog})
					case "/v1/chat/completions":
						invocations.Add(1)
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(completedChat))
					default:
						http.NotFound(w, r)
					}
				}))
				defer adapter.Close()
				t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
				t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "test-management")
				t.Setenv("CLIPROXYAPI_KEY", "test-service")
				accounts := []config.Account{
					{ID: "cliproxy-codex", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: adapter.URL + "/v1", APIKeyEnv: "CLIPROXYAPI_KEY", Enabled: true, Models: []string{models["a"], models["b"]}, Capabilities: config.InferCodexCapabilities([]string{models["a"], models["b"]})},
					{ID: "cliproxy-claude-code", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: adapter.URL + "/v1", APIKeyEnv: "CLIPROXYAPI_KEY", Enabled: true, Models: []string{models["claude"]}},
					{ID: "cliproxy-antigravity", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: adapter.URL + "/v1", APIKeyEnv: "CLIPROXYAPI_KEY", Enabled: true, Models: []string{models["anti"]}},
				}
				route := config.Route{Model: "sol", Targets: []config.RouteTarget{{Account: "cliproxy-codex"}}}
				if routeKind == "legacy" {
					route = config.Route{Accounts: []string{"cliproxy-codex"}, UpstreamModel: models["a"]}
				}
				g := newTestGateway(t, accounts, map[string]config.Route{"reviewed-alias": route})
				cookie, csrf := loginAdminForTest(t, g)
				for _, id := range []string{"a", "b"} {
					method, path, body := http.MethodDelete, "/admin/api/oauth/accounts", `{"id":"`+id+`"}`
					if action == "disable" {
						method, path, body = http.MethodPost, "/admin/api/oauth/accounts/status", `{"id":"`+id+`","disabled":true}`
					}
					w := httptest.NewRecorder()
					g.ServeAdminAPI(w, adminOAuthRequest(method, path, body, cookie, csrf))
					if w.Code != http.StatusOK {
						t.Fatalf("mutation status=%d body=%s", w.Code, w.Body.String())
					}
					if id == "a" && !strings.Contains(w.Body.String(), "reviewed routes") {
						t.Fatal("missing explicit route repair warning")
					}
					current := g.Config()
					if current.Accounts[0].Enabled {
						t.Fatal("removed routed model kept its source enabled")
					}
					if _, exists := current.Routes["reviewed-alias"]; !exists {
						t.Fatal("credential maintenance removed reviewed alias")
					}
					for index, want := range map[int]string{1: models["claude"], 2: models["anti"]} {
						account := current.Accounts[index]
						if !account.Enabled || len(account.Models) != 1 || account.Models[0] != want {
							t.Fatalf("unrelated provider scope changed: id=%s enabled=%v models=%v", account.ID, account.Enabled, account.Models)
						}
					}
					denied := httptest.NewRecorder()
					g.ServeGateway(denied, gatewayRequest(`{"model":"reviewed-alias","messages":[{"role":"user","content":"ping"}]}`))
					if denied.Code == 200 || invocations.Load() != 0 {
						t.Fatalf("removed routed model reached adapter: status=%d calls=%d", denied.Code, invocations.Load())
					}
					for _, model := range g.state.Load().models {
						if model == "reviewed-alias" || model == "sol" {
							t.Fatal("disabled source remains in client model catalog")
						}
					}
				}
				allowed := httptest.NewRecorder()
				g.ServeGateway(allowed, gatewayRequest(`{"model":"claude-code/claude-fixture","messages":[{"role":"user","content":"ping"}]}`))
				if allowed.Code != 200 || invocations.Load() != 1 {
					t.Fatalf("unrelated provider stopped: status=%d calls=%d", allowed.Code, invocations.Load())
				}
			})
		}
	}
}

func TestOAuthPoolEnsurePreservesProviderScopeAndManualDisable(t *testing.T) {
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"data": []map[string]string{{"id": "gpt-5.6-sol"}, {"id": "claude-code/claude-fixture"}, {"id": "antigravity/claude-fixture"}}})
	}))
	defer adapter.Close()
	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
	t.Setenv("CLIPROXYAPI_KEY", "test-service")
	g := newTestGateway(t, []config.Account{
		{ID: "cliproxy-antigravity", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: adapter.URL + "/v1", APIKeyEnv: "CLIPROXYAPI_KEY", Models: []string{"antigravity/claude-fixture"}, Enabled: true},
		{ID: "cliproxy-codex", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: adapter.URL + "/v1", APIKeyEnv: "CLIPROXYAPI_KEY", Enabled: false},
	}, nil)
	ready, err := g.ensureOAuthPoolAccount(context.Background(), "codex")
	if err != nil || ready {
		t.Fatalf("manually disabled source was enabled: ready=%v err=%v", ready, err)
	}
	cfg := g.Config()
	if len(cfg.Accounts) != 2 || cfg.Accounts[1].Enabled || len(cfg.Accounts[1].Models) != 1 || cfg.Accounts[1].Models[0] != "gpt-5.6-sol" || len(cfg.Accounts[0].Models) != 1 {
		t.Fatal("provider catalog widened or disabled state lost")
	}
	if ready, err := g.ensureOAuthPoolAccount(context.Background(), "anthropic"); err == nil || ready || len(g.Config().Accounts) != 2 {
		t.Fatal("unreviewed broad pool created alongside scoped connections")
	}
}
