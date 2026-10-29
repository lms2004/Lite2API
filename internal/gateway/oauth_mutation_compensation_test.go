package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestOAuthCredentialMutationCompensatesFailure(t *testing.T) {
	for _, action := range []string{"delete", "delete_disabled", "disable", "enable"} {
		for _, failure := range []string{"store", "adapter_http_error", "transport_error", "cancelled", "cancelled_after_apply", "rollback_failure", "rollback_unconfirmed"} {
			t.Run(action+"/"+failure, func(t *testing.T) {
				originalDisabled := action == "enable" || action == "delete_disabled"
				isDelete := strings.HasPrefix(action, "delete")
				original, _ := json.Marshal(map[string]any{"type": "codex", "access_token": "fixture-original-access", "refresh_token": "fixture-original-refresh", "disabled": originalDisabled, "prefix": "preserved"})
				var mu sync.Mutex
				file := append(json.RawMessage(nil), original...)
				disabled := originalDisabled
				attempted, restoring := false, false
				var invocationCount atomic.Int64
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					if r.Header.Get("Authorization") != "Bearer test-management" && strings.HasPrefix(r.URL.Path, "/v0/") {
						t.Error("management auth missing")
					}
					firstMutation := !attempted && ((isDelete && r.Method == http.MethodDelete) || (!isDelete && r.Method == http.MethodPatch))
					recoveryMutation := attempted && (r.Method == http.MethodPost || r.Method == http.MethodPatch)
					if recoveryMutation {
						restoring = true
						if failure == "rollback_failure" {
							http.Error(w, "fixture recovery failure", http.StatusServiceUnavailable)
							return
						}
					}
					switch r.URL.Path {
					case "/v0/management/auth-files/download":
						if file == nil {
							http.NotFound(w, r)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write(file)
					case "/v0/management/auth-files":
						switch r.Method {
						case http.MethodGet:
							if attempted && !restoring && failure == "cancelled_after_apply" {
								cancel()
							}
							if restoring && failure == "rollback_unconfirmed" {
								http.Error(w, "fixture cannot verify", 503)
								return
							}
							files := []map[string]any{}
							if file != nil {
								files = append(files, map[string]any{"name": "fixture.json", "id": "fixture.json", "auth_index": "stable-index", "provider": "codex", "status": "active", "disabled": disabled})
							}
							writeJSON(w, 200, map[string]any{"files": files})
						case http.MethodDelete:
							file = nil
						case http.MethodPost:
							if r.URL.Query().Get("name") != "fixture.json" {
								t.Error("rollback used wrong file name")
							}
							if err := json.NewDecoder(r.Body).Decode(&file); err != nil {
								t.Error(err)
							}
							disabled = false // Real adapter registers uploaded auth files as active.
						}
					case "/v0/management/auth-files/status":
						var body struct {
							Name     string `json:"name"`
							Disabled bool   `json:"disabled"`
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if body.Name != "fixture.json" {
							t.Error("status used wrong auth file")
						}
						disabled = body.Disabled
					case "/v1/models":
						models := []map[string]string{}
						if file != nil && !disabled {
							models = append(models, map[string]string{"id": "fixture-model"})
						}
						writeJSON(w, 200, map[string]any{"data": models})
					case "/v1/chat/completions":
						invocationCount.Add(1)
						if file == nil || disabled {
							http.Error(w, "fixture source unavailable", 401)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(completedChat))
						return
					default:
						http.NotFound(w, r)
					}
					if firstMutation {
						attempted = true
						switch failure {
						case "adapter_http_error":
							http.Error(w, "mutation applied before fixture failure", 500)
							return
						case "transport_error":
							conn, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							_ = conn.Close()
							return
						case "cancelled":
							cancel()
						}
					}
					if r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodDelete {
						writeJSON(w, 200, map[string]any{"status": "ok"})
					}
				}))
				defer adapter.Close()
				t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
				t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "test-management")
				t.Setenv("CLIPROXYAPI_KEY", "test-service")
				models := []string{"fixture-model"}
				if action == "enable" {
					models = nil
				}
				account := config.Account{ID: "pool", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: adapter.URL + "/v1", APIKeyEnv: "CLIPROXYAPI_KEY", Enabled: !originalDisabled, Models: models}
				g := newTestGateway(t, []config.Account{account}, nil)
				beforeConfig := g.Config()
				beforeDisk, err := os.ReadFile(g.configPath)
				if err != nil {
					t.Fatal(err)
				}
				if failure == "store" || failure == "rollback_failure" || failure == "rollback_unconfirmed" {
					// Keep the readable effective config intact while forcing SaveEffective
					// to fail on a directory path, regardless of test user privileges.
					g.store = config.NewStore(t.TempDir())
				}
				cookie, csrf := loginAdminForTest(t, g)
				method, path, body := http.MethodDelete, "/admin/api/oauth/accounts", `{"id":"stable-index"}`
				if !isDelete {
					method, path = http.MethodPost, "/admin/api/oauth/accounts/status"
					body = `{"id":"stable-index","disabled":true}`
					if action == "enable" {
						body = `{"id":"stable-index","disabled":false}`
					}
				}
				response := httptest.NewRecorder()
				request := adminOAuthRequest(method, path, body, cookie, csrf).WithContext(ctx)
				g.ServeAdminAPI(response, request)
				wantCode := "oauth_credential_change_rolled_back"
				incomplete := failure == "rollback_failure" || failure == "rollback_unconfirmed"
				if incomplete {
					wantCode = "oauth_credential_rollback_incomplete"
				}
				if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), wantCode) {
					t.Fatalf("failure response status=%d body=%s", response.Code, response.Body.String())
				}
				if strings.Contains(response.Body.String(), "fixture-original") {
					t.Fatal("credential leaked in error response")
				}
				afterDisk, err := os.ReadFile(g.configPath)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(beforeDisk, afterDisk) || !sameEffectiveConfig(beforeConfig, g.Config()) {
					t.Fatal("failed credential mutation changed gateway state")
				}
				mu.Lock()
				if !attempted {
					t.Error("fixture mutation did not run")
				}
				if !incomplete && (!bytes.Equal(file, original) || disabled != originalDisabled) {
					t.Error("original credential file/status was not restored")
				}
				mu.Unlock()
				if incomplete {
					if strings.Contains(response.Body.String(), "were restored") {
						t.Fatal("unconfirmed recovery was claimed successful")
					}
					return
				}
				// Recovery is also checked through the next actual gateway request to the
				// fixture; a rollback acknowledgement alone is not the success assertion.
				next := httptest.NewRecorder()
				g.ServeGateway(next, gatewayRequest(`{"model":"fixture-model","messages":[{"role":"user","content":"ping"}]}`))
				if originalDisabled {
					if next.Code == 200 || invocationCount.Load() != 0 {
						t.Fatal("originally disabled source became callable")
					}
				} else if next.Code != 200 || invocationCount.Load() != 1 {
					t.Fatalf("restored source did not complete next call: status=%d calls=%d", next.Code, invocationCount.Load())
				}
			})
		}
	}
}

func TestOAuthCredentialDeleteRequiresSnapshotBeforeMutation(t *testing.T) {
	var deletes atomic.Int64
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
		}
		if r.URL.Path == "/v0/management/auth-files" {
			writeJSON(w, 200, map[string]any{"files": []map[string]any{{"name": "fixture.json", "auth_index": "stable", "provider": "codex", "status": "active"}}})
			return
		}
		http.Error(w, "snapshot unavailable", 503)
	}))
	defer adapter.Close()
	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "test-management")
	g := newTestGateway(t, nil, nil)
	cookie, csrf := loginAdminForTest(t, g)
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, adminOAuthRequest(http.MethodDelete, "/admin/api/oauth/accounts", `{"id":"stable"}`, cookie, csrf))
	if w.Code != http.StatusBadGateway || deletes.Load() != 0 {
		t.Fatalf("delete occurred without restorable snapshot: status=%d calls=%d", w.Code, deletes.Load())
	}
}

func TestOAuthCredentialMutationTargetVerifiesRuntimeParent(t *testing.T) {
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"files": []map[string]any{{"id": "virtual-project", "auth_index": "virtual-index", "provider": "gemini-cli", "runtime_only": true, "path": "/private/adapter/auths/gemini-parent.json", "disabled": true}}})
	}))
	defer adapter.Close()
	t.Setenv("CLIPROXYAPI_MANAGEMENT_URL", adapter.URL)
	t.Setenv("CLIPROXYAPI_MANAGEMENT_KEY", "test-management")
	for _, id := range []string{"virtual-index", "gemini-parent.json"} {
		name, disabled, err := oauthCredentialMutationTarget(context.Background(), id)
		if err != nil || name != "gemini-parent.json" || !disabled {
			t.Fatalf("runtime parent target=%q disabled=%v err=%v", name, disabled, err)
		}
	}
}
