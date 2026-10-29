package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func TestPlainSuccessCannotHideUnrepairedToolContract(t *testing.T) {
	var repaired atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if repaired.Load() {
			_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`))
			return
		}
		_, _ = w.Write([]byte(completedChat))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Weight: 1}}, map[string]config.Route{"daily": {Accounts: []string{"a"}, UpstreamModel: "m"}})
	tools := `{"model":"daily","messages":[],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"required"}`
	failure := httptest.NewRecorder()
	g.ServeGateway(failure, gatewayRequest(tools))
	if failure.Code != 502 {
		t.Fatalf("forced tool mismatch was accepted: %d", failure.Code)
	}
	plain := httptest.NewRecorder()
	g.ServeGateway(plain, gatewayRequest(`{"model":"daily","messages":[]}`))
	if plain.Code != 200 {
		t.Fatalf("ordinary call failed: %d", plain.Code)
	}
	ready := httptest.NewRecorder()
	g.serveReadiness(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != 503 || len(g.Stats().RouteLatest) != 2 {
		t.Fatalf("plain response hid tool failure: %d %s", ready.Code, ready.Body.String())
	}
	operations := buildOperationsSnapshot(time.Now(), g.Config(), g.Accounts(), g.Stats())
	if len(operations.Routes[0].Observations) != 2 {
		t.Fatal("missing scoped maintenance evidence")
	}
	repaired.Store(true)
	retry := httptest.NewRecorder()
	g.ServeGateway(retry, gatewayRequest(tools))
	if retry.Code != 200 {
		t.Fatalf("same-scope repair failed: %d %s", retry.Code, retry.Body.String())
	}
	ready = httptest.NewRecorder()
	g.serveReadiness(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != 200 {
		t.Fatalf("verified repair did not clear its own failure: %s", ready.Body.String())
	}
}

func TestObservationScopeIncludesCredentialModelAndRequirements(t *testing.T) {
	s := NewStats(8)
	s.ConfigureRouteFingerprints(map[string]string{"m": "generation"})
	base := RequestRecord{Time: time.Now().UTC().Format(time.RFC3339Nano), Model: "m", AccountID: "pool", Operation: config.OperationOpenAIChat, UpstreamModel: "real", CredentialID: "0123456789abcdef", Outcome: "success", RouteFingerprint: "generation"}
	for i := 0; i < 5; i++ {
		row := base
		switch i {
		case 1:
			row.CredentialID = "fedcba9876543210"
		case 2:
			row.UpstreamModel = "different"
		case 3:
			row.RequiredFeatures = "tools"
		case 4:
			row.RequestContract = strings.Repeat("a", 64)
		}
		s.RecordRouteObservation(row)
	}
	if len(s.Snapshot().RouteLatest) != 5 {
		t.Fatal("one affected scope overwrote another")
	}
}
