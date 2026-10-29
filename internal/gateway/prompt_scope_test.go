package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestPromptTestEnforcesDeclaredModelBeforeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, model, upstream string
		models                []string
		modelMap              map[string]string
		allowed               bool
	}{
		{name: "undeclared", model: "not-declared", models: []string{"known"}},
		{name: "literal wildcard", model: "*", models: []string{"*"}},
		{name: "declared", model: "known", upstream: "known", models: []string{"known"}, allowed: true},
		{name: "model map alias", model: "alias", upstream: "real", models: []string{"known"}, modelMap: map[string]string{"alias": "real"}, allowed: true},
		{name: "model map target", model: "real", upstream: "real", models: []string{"known"}, modelMap: map[string]string{"alias": "real"}, allowed: true},
		{name: "wildcard scope", model: "concrete", upstream: "concrete", models: []string{"*"}, allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct{ Model string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != tc.upstream {
					t.Errorf("unexpected upstream model=%q err=%v", body.Model, err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer upstream.Close()
			g := newTestGateway(t, []config.Account{{ID: "source", Type: "openai", APIKey: "fixture-secret", BaseURL: upstream.URL, Models: tc.models, ModelMap: tc.modelMap, Operations: []string{config.OperationOpenAIChat}, Enabled: true, Concurrency: 1}}, nil)
			account := g.state.Load().scheduler.Get("source")
			if !tc.allowed {
				// Model rejection precedes admission even when the source is full.
				if !account.tryAcquire() {
					t.Fatal("could not hold source capacity")
				}
				defer account.release()
			}
			body, _ := json.Marshal(promptTestRequest{AccountID: "source", Model: tc.model, Messages: []promptTestMessage{{Role: "user", Content: "test"}}})
			req := httptest.NewRequest(http.MethodPost, "/admin/api/prompt-test", strings.NewReader(string(body)))
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Authorization", "Bearer admin-secret")
			w := httptest.NewRecorder()
			g.ServeAdminAPI(w, req)
			if tc.allowed {
				if w.Code != http.StatusOK || calls.Load() != 1 || account.state.capacity.active.Load() != 0 {
					t.Fatalf("allowed model: status=%d calls=%d active=%d body=%s", w.Code, calls.Load(), account.state.capacity.active.Load(), w.Body.String())
				}
			} else if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "prompt_test_model_unsupported") || calls.Load() != 0 || account.state.capacity.active.Load() != 1 {
				t.Fatalf("undeclared model reached admission or upstream: status=%d calls=%d active=%d body=%s", w.Code, calls.Load(), account.state.capacity.active.Load(), w.Body.String())
			}
		})
	}
}
