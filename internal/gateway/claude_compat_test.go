package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestAnthropicMessagesRejectUnsupportedReasoningAndPreserveNativeThinking(t *testing.T) {
	var upstreamBody map[string]any
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("X-Api-Key") != "upstream-secret" {
			t.Error("native protocol headers were lost")
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`))
	}))
	defer upstream.Close()
	accounts := []config.Account{{ID: "anthropic-main", Type: "anthropic", BaseURL: upstream.URL + "/v1", APIKey: "upstream-secret", Models: []string{"real-model"}, Operations: []string{config.OperationAnthropic}, Concurrency: 1, Weight: 1, Enabled: true}}
	g := newTestGateway(t, accounts, map[string]config.Route{"claude": {Targets: []config.RouteTarget{{Account: "anthropic-main", Model: "real-model"}}}})
	for _, item := range []struct {
		body string
		want int
	}{
		{`{"model":"claude","max_tokens":64,"reasoning_effort":"high","messages":[{"role":"user","content":"hello"}]}`, http.StatusBadRequest},
		{`{"model":"claude","max_tokens":64,"thinking":{"type":"enabled","budget_tokens":32},"messages":[{"role":"user","content":"hello"}]}`, http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(item.body))
		req.Header.Set("Authorization", "Bearer gateway-secret")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("anthropic-version", "2023-06-01")
		w := httptest.NewRecorder()
		g.ServeGateway(w, req)
		if w.Code != item.want {
			t.Fatalf("status=%d want=%d body=%s", w.Code, item.want, w.Body.String())
		}
	}
	if calls != 1 || upstreamBody["model"] != "real-model" || upstreamBody["thinking"] == nil {
		t.Fatalf("native request not preserved: calls=%d body=%+v", calls, upstreamBody)
	}
}

func TestAnthropicCountTokensIsForwarded(t *testing.T) {
	var observedPath string
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":7}`))
	}))
	defer upstream.Close()

	accounts := []config.Account{{
		ID: "anthropic-main", Type: "anthropic", BaseURL: upstream.URL + "/v1", APIKey: "upstream-secret",
		Models: []string{"real-model"}, Operations: []string{config.OperationAnthropic}, Concurrency: 1, Weight: 1, Enabled: true,
	}}
	routes := map[string]config.Route{
		"claude": {Targets: []config.RouteTarget{{Account: "anthropic-main", Model: "real-model"}}},
	}
	g := newTestGateway(t, accounts, routes)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{
		"model":"claude",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Authorization", "Bearer gateway-secret")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	g.ServeGateway(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if observedPath != "/v1/messages/count_tokens" {
		t.Fatalf("upstream path=%q", observedPath)
	}
	if upstreamBody["model"] != "real-model" {
		t.Fatalf("upstream model=%v", upstreamBody["model"])
	}
	if !strings.Contains(w.Body.String(), `"input_tokens":7`) {
		t.Fatalf("body=%s", w.Body.String())
	}
}
