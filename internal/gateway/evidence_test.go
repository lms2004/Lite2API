package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func TestReadinessDoesNotHideFreshFailureInAnotherOperation(t *testing.T) {
	account := config.Account{ID: "upstream", Type: "openai", BaseURL: "https://api.example.com/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Concurrency: 4, Weight: 1}
	g := newTestGateway(t, []config.Account{account}, map[string]config.Route{"m": {Accounts: []string{"upstream"}}})
	now := time.Now().UTC()
	fingerprint := g.state.Load().routeFingerprints["m"]
	failure := RequestRecord{Time: now.Add(-time.Second).Format(time.RFC3339Nano), Model: "m", AccountID: "upstream", Operation: config.OperationOpenAIResponses, Status: 502, Outcome: "incomplete_response", RouteFingerprint: fingerprint}
	success := failure
	success.Time, success.Operation, success.Status, success.Outcome = now.Format(time.RFC3339Nano), config.OperationOpenAIChat, 200, "success"
	g.stats.RecordRouteObservation(failure)
	g.stats.RecordRouteObservation(success)
	if len(g.Stats().RouteLatest) != 2 {
		t.Fatal("one interface erased the other interface's evidence")
	}
	w := httptest.NewRecorder()
	g.serveReadiness(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"failed_routes":["m"]`) {
		t.Fatalf("fresh Responses failure hidden by chat success: %d %s", w.Code, w.Body.String())
	}
	failure.Time, failure.Status, failure.Outcome = now.Add(time.Millisecond).Format(time.RFC3339Nano), 200, "success"
	g.stats.RecordRouteObservation(failure)
	w = httptest.NewRecorder()
	g.serveReadiness(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != 200 {
		t.Fatalf("same-scope recovery did not clear failed evidence: %d %s", w.Code, w.Body.String())
	}
}

func TestRequestEvidenceScopesSurviveRecoveryAndReload(t *testing.T) {
	s := NewStats(1)
	s.ConfigureRouteFingerprints(map[string]string{"m": "old"})
	path := filepath.Join(t.TempDir(), "request.log")
	var contents []byte
	for _, item := range []struct {
		account, operation string
		stream             bool
	}{{"a", config.OperationOpenAIChat, false}, {"b", config.OperationOpenAIChat, false}, {"a", config.OperationOpenAIResponses, false}, {"a", config.OperationOpenAIChat, true}} {
		r := RequestRecord{Time: time.Now().UTC().Format(time.RFC3339Nano), Model: "m", AccountID: item.account, Operation: item.operation, Stream: item.stream, Outcome: "success", Status: 200, RouteFingerprint: "old"}
		s.RecordRouteObservation(r)
		data, _ := json.Marshal(r)
		contents = append(contents, append(data, '\n')...)
	}
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	_, recovered, err := loadRequestState(path, 0, 1, map[string]string{"m": "old"})
	if err != nil || len(recovered) != 4 || len(s.Snapshot().RouteLatest) != 4 {
		t.Fatalf("scoped evidence lost: recovered=%d memory=%d error=%v", len(recovered), len(s.Snapshot().RouteLatest), err)
	}
	s.ConfigureRouteFingerprints(map[string]string{"m": "new"})
	if len(s.Snapshot().RouteLatest) != 0 {
		t.Fatal("old configuration still proves replacement route")
	}
}

func TestAttemptEvidenceIsBoundedAndImmutable(t *testing.T) {
	s := NewStats(1)
	r := RequestRecord{Time: time.Now().UTC().Format(time.RFC3339Nano), Attempts: make([]UpstreamAttempt, maxObservedAttempts+1), AttemptCount: maxObservedAttempts + 1}
	r.Attempts[0].AccountID = "original"
	s.Record(r)
	r.Attempts[0].AccountID = "mutated input"
	first := s.Snapshot()
	if len(first.Recent[0].Attempts) != maxObservedAttempts || first.Recent[0].Attempts[0].AccountID != "original" {
		t.Fatal("attempt evidence aliased input or exceeded bound")
	}
	first.Recent[0].Attempts[0].AccountID = "mutated snapshot"
	if s.Snapshot().Recent[0].Attempts[0].AccountID != "original" {
		t.Fatal("snapshot can mutate retained evidence")
	}
	s.ConfigureRouteFingerprints(map[string]string{"m": "generation"})
	r.Model, r.AccountID, r.RouteFingerprint, r.Outcome = "m", "a", "generation", "success"
	s.RecordRouteObservation(r)
	if len(s.Snapshot().RouteLatest[0].Attempts) != 0 {
		t.Fatal("health evidence replicated detailed attempt allocations")
	}
}

func TestGatewayExplainsUnavailableSourcesAndCapacity(t *testing.T) {
	for _, name := range []string{"no_eligible_source", "source_unavailable", "capacity_exhausted", "queue_timeout"} {
		t.Run(name, func(t *testing.T) {
			account := config.Account{ID: "a", Type: "openai", BaseURL: "https://example.invalid/v1", APIKey: "test", Models: []string{"m"}, Operations: []string{config.OperationOpenAIChat}, Enabled: true, Concurrency: 1, Weight: 1}
			if name == "no_eligible_source" {
				account.Operations = []string{config.OperationEmbeddings}
			}
			g := newTestGateway(t, []config.Account{account}, nil)
			runtime := g.state.Load().scheduler.Get("a")
			if name == "source_unavailable" {
				runtime.reportFailure(runtime.beginAttempt(config.OperationOpenAIChat, "m"), "HTTP 429", 1, time.Minute, true)
			}
			if name == "capacity_exhausted" || name == "queue_timeout" {
				if !runtime.tryAcquire() {
					t.Fatal("fixture could not occupy capacity")
				}
				defer runtime.release()
			}
			if name == "queue_timeout" {
				cfg := g.Config()
				cfg.Server.QueueTimeout = config.Duration{Duration: 5 * time.Millisecond}
				if err := config.NewStore(g.configPath).Save(cfg); err != nil {
					t.Fatal(err)
				}
				if err := g.Reload(); err != nil {
					t.Fatal(err)
				}
			}
			w := httptest.NewRecorder()
			g.ServeGateway(w, gatewayRequest(`{"model":"m","messages":[]}`))
			if w.Code != 503 || !strings.Contains(w.Body.String(), `"code":"`+name+`"`) || g.Stats().Recent[0].Outcome != name {
				t.Fatalf("failure classification lost: status=%d body=%s recent=%+v", w.Code, w.Body.String(), g.Stats().Recent)
			}
		})
	}
}

func TestQueuedCancellationRemainsClientCancellation(t *testing.T) {
	g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: "https://example.invalid/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Concurrency: 1, Weight: 1}}, nil)
	account := g.state.Load().scheduler.Get("a")
	if !account.tryAcquire() {
		t.Fatal("fixture failed to occupy capacity")
	}
	defer account.release()
	cfg := g.Config()
	cfg.Server.QueueTimeout = config.Duration{Duration: time.Second}
	if err := config.NewStore(g.configPath).Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.ServeGateway(httptest.NewRecorder(), gatewayRequest(`{"model":"m","messages":[]}`).WithContext(ctx))
	recent := g.Stats().Recent
	if len(recent) != 1 || recent[0].Status != 499 || recent[0].Outcome != "client_cancelled" || recent[0].AttemptCount != 0 {
		t.Fatalf("canceled waiter classified as unavailable or submitted upstream: %+v", recent)
	}
}

func TestPromptTestRequiresCompleteProtocolAndRedactsUpstreamErrors(t *testing.T) {
	for _, item := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"invalid JSON shape", 200, `{"ok":true}`, 502},
		{"incomplete chat", 200, `{"choices":[{"message":{"content":"partial"}}]}`, 502},
		{"credential error", 401, `{"error":{"message":"access_token=private-secret"}}`, 502},
		{"complete chat", 200, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, 200},
	} {
		t.Run(item.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(item.status)
				_, _ = w.Write([]byte(item.body))
			}))
			defer upstream.Close()
			g := newTestGateway(t, []config.Account{{ID: "a", Type: "openai", BaseURL: upstream.URL + "/v1", APIKey: "test", Models: []string{"m"}, Enabled: true, Concurrency: 1, Weight: 1}}, nil)
			w := httptest.NewRecorder()
			g.servePromptTest(w, httptest.NewRequest(http.MethodPost, "/admin/api/accounts/prompt-test", strings.NewReader(`{"account_id":"a","model":"m","messages":[{"role":"user","content":"ping"}]}`)), g.state.Load())
			if w.Code != item.want || strings.Contains(w.Body.String(), "private-secret") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
