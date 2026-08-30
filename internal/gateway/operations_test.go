package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func TestOperationsNoSamplesRemainUnknown(t *testing.T) {
	now := time.Now().UTC()
	cfg := config.Defaults()
	cfg.Accounts = []config.Account{{ID: "upstream", Enabled: true, Concurrency: 4}}
	cfg.Routes = map[string]config.Route{"chat": {Accounts: []string{"upstream"}}}
	snapshot := buildOperationsSnapshot(now, cfg, []AccountSnapshot{{ID: "upstream", Enabled: true, Concurrency: 4}}, StatsSnapshot{})
	if snapshot.State != HealthUnknown || snapshot.Window.SuccessRate != nil || snapshot.Window.P95LatencyMS != nil {
		t.Fatalf("empty snapshot must stay unknown without synthetic metrics: %+v", snapshot)
	}
}

func TestOperationsUsesLatestRealDataRegardlessOfAge(t *testing.T) {
	now := time.Now().UTC()
	record := RequestRecord{Time: now.Add(-72 * time.Hour).Format(time.RFC3339Nano), Model: "chat", AccountID: "upstream", Status: 200, LatencyMS: 12}
	cfg := config.Defaults()
	cfg.Accounts = []config.Account{{ID: "upstream", Enabled: true, Concurrency: 4}}
	cfg.Routes = map[string]config.Route{"chat": {Accounts: []string{"upstream"}}}
	snapshot := buildOperationsSnapshot(now, cfg, []AccountSnapshot{{ID: "upstream", Enabled: true, Concurrency: 4}}, StatsSnapshot{Recent: []RequestRecord{record}, RouteLatest: []RequestRecord{record}})
	if snapshot.Window.Samples != 1 || snapshot.Routes[0].Window.Samples != 1 || snapshot.State != HealthReady {
		t.Fatalf("latest real data must remain valid regardless of age: %+v", snapshot)
	}
	if snapshot.Window.ObservedAt != record.Time || snapshot.Window.SuccessRate == nil || *snapshot.Window.SuccessRate != 1 || snapshot.Window.P95LatencyMS == nil || *snapshot.Window.P95LatencyMS != 12 {
		t.Fatalf("latest real data must drive metrics: %+v", snapshot.Window)
	}
}

func TestOperationsMetricsUseOnlyLatestRecord(t *testing.T) {
	now := time.Now().UTC()
	cfg := config.Defaults()
	cfg.Accounts = []config.Account{{ID: "upstream", Enabled: true, Concurrency: 4}}
	cfg.Routes = map[string]config.Route{"chat": {Accounts: []string{"upstream"}}}
	records := []RequestRecord{
		{Time: now.Add(-48 * time.Hour).Format(time.RFC3339Nano), Model: "chat", AccountID: "upstream", Status: 503, LatencyMS: 9000},
		{Time: now.Add(-24 * time.Hour).Format(time.RFC3339Nano), Model: "chat", AccountID: "upstream", Status: 200, LatencyMS: 42},
	}
	snapshot := buildOperationsSnapshot(now, cfg, []AccountSnapshot{{ID: "upstream", Enabled: true, Concurrency: 4}}, StatsSnapshot{Recent: records, RouteLatest: []RequestRecord{records[1]}})
	if snapshot.Window.Samples != 1 || snapshot.Window.Successful != 1 || snapshot.Window.P95LatencyMS == nil || *snapshot.Window.P95LatencyMS != 42 || snapshot.State != HealthReady {
		t.Fatalf("only latest record should drive health metrics: %+v", snapshot)
	}
}

func TestOperationsMissingRouteTargetIsUnavailable(t *testing.T) {
	cfg := config.Defaults()
	cfg.Routes = map[string]config.Route{"chat": {Targets: []config.RouteTarget{{Account: "missing"}}}}
	snapshot := buildOperationsSnapshot(time.Now().UTC(), cfg, nil, StatsSnapshot{})
	if snapshot.State != HealthUnavailable || snapshot.Routes[0].State != HealthUnavailable || len(snapshot.Routes[0].MissingTargets) != 1 {
		t.Fatalf("missing target must fail route coverage: %+v", snapshot)
	}
}

func TestOperationsUnlimitedConcurrencyHasNoUtilization(t *testing.T) {
	accounts := []AccountSnapshot{{ID: "unlimited", Enabled: true, Concurrency: 0, Active: 7}}
	snapshot := buildOperationsSnapshot(time.Now().UTC(), config.Defaults(), accounts, StatsSnapshot{Active: 7})
	if !snapshot.Capacity.Unlimited || snapshot.Capacity.Limit != nil || snapshot.Capacity.Utilization != nil {
		t.Fatalf("unlimited capacity must not produce a percentage: %+v", snapshot.Capacity)
	}
}

func TestOperationsChecksEveryConfiguredRoute(t *testing.T) {
	now := time.Now().UTC()
	cfg := config.Defaults()
	cfg.Accounts = []config.Account{{ID: "upstream", Enabled: true, Concurrency: 4}}
	cfg.Routes = map[string]config.Route{
		"ready":  {Accounts: []string{"upstream"}},
		"broken": {Targets: []config.RouteTarget{{Account: "missing"}}},
	}
	record := RequestRecord{Time: now.Format(time.RFC3339Nano), Model: "ready", AccountID: "upstream", Status: 200, LatencyMS: 25}
	snapshot := buildOperationsSnapshot(now, cfg, []AccountSnapshot{{ID: "upstream", Enabled: true, Concurrency: 4}}, StatsSnapshot{Recent: []RequestRecord{record}, RouteLatest: []RequestRecord{record}})
	if snapshot.State != HealthUnavailable || len(snapshot.Routes) != 2 {
		t.Fatalf("global health must include broken route coverage: %+v", snapshot)
	}
}

func TestOperationsRecentFailureIsDegraded(t *testing.T) {
	now := time.Now().UTC()
	cfg := config.Defaults()
	cfg.Accounts = []config.Account{{ID: "upstream", Enabled: true, Concurrency: 4}}
	cfg.Routes = map[string]config.Route{"chat": {Accounts: []string{"upstream"}}}
	record := RequestRecord{Time: now.Format(time.RFC3339Nano), Model: "chat", AccountID: "upstream", Status: 503, LatencyMS: 25}
	snapshot := buildOperationsSnapshot(now, cfg, []AccountSnapshot{{ID: "upstream", Enabled: true, Concurrency: 4}}, StatsSnapshot{Recent: []RequestRecord{record}, RouteLatest: []RequestRecord{record}})
	if snapshot.State != HealthDegraded || snapshot.Routes[0].State != HealthDegraded || snapshot.Routes[0].DegradedTargets != 1 {
		t.Fatalf("an eligible target with recent failures must be degraded: %+v", snapshot)
	}
}

func TestHealthEndpointUsesRouteCoverage(t *testing.T) {
	accounts := []config.Account{{ID: "disabled", Type: "openai", BaseURL: "https://api.example.com/v1", APIKey: "test", Models: []string{"chat"}, Concurrency: 4, Weight: 1, Enabled: false}}
	g := newTestGateway(t, accounts, map[string]config.Route{"chat": {Accounts: []string{"disabled"}}})
	recorder := httptest.NewRecorder()
	g.serveHealth(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable route coverage must return 503, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Liveness string      `json:"liveness"`
		Status   HealthState `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Liveness != "ok" || body.Status != HealthUnavailable {
		t.Fatalf("health endpoint must separate liveness from route readiness: %+v", body)
	}
}

func TestLivenessAndReadinessAreSeparate(t *testing.T) {
	accounts := []config.Account{{ID: "upstream", Type: "openai", BaseURL: "https://api.example.com/v1", APIKey: "test", Models: []string{"chat"}, Concurrency: 4, Weight: 1, Enabled: true}}
	g := newTestGateway(t, accounts, map[string]config.Route{"chat": {Accounts: []string{"upstream"}}})
	live := httptest.NewRecorder()
	g.serveLiveness(live, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("liveness=%d body=%s", live.Code, live.Body.String())
	}
	ready := httptest.NewRecorder()
	g.serveReadiness(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"unobserved_routes":["chat"]`) {
		t.Fatalf("unobserved route readiness=%d body=%s", ready.Code, ready.Body.String())
	}

	fingerprint := g.state.Load().routeFingerprints["chat"]
	success := RequestRecord{Time: time.Now().UTC().Format(time.RFC3339Nano), Model: "chat", AccountID: "upstream", Status: 200, Outcome: "success", RouteFingerprint: fingerprint}
	g.stats.Record(success)
	g.stats.RecordRouteObservation(success)
	ready = httptest.NewRecorder()
	g.serveReadiness(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("fresh route readiness=%d body=%s", ready.Code, ready.Body.String())
	}

	failure := RequestRecord{Time: time.Now().UTC().Add(time.Millisecond).Format(time.RFC3339Nano), Model: "chat", AccountID: "upstream", Status: 500, Outcome: "uncertain_submission", RouteFingerprint: fingerprint}
	g.stats.Record(failure)
	g.stats.RecordRouteObservation(failure)
	ready = httptest.NewRecorder()
	g.serveReadiness(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable || !strings.Contains(ready.Body.String(), `"failed_routes":["chat"]`) {
		t.Fatalf("fresh failed route readiness=%d body=%s", ready.Code, ready.Body.String())
	}
}

func TestReadinessRetainsLowVolumeRouteObservationBeyondRecentRing(t *testing.T) {
	accounts := []config.Account{{
		ID: "upstream", Type: "openai", BaseURL: "https://api.example.com/v1", APIKey: "test",
		Models: []string{"hot", "cold"}, Concurrency: 4, Weight: 1, Enabled: true,
	}}
	routes := map[string]config.Route{
		"hot":  {Accounts: []string{"upstream"}, UpstreamModel: "hot"},
		"cold": {Accounts: []string{"upstream"}, UpstreamModel: "cold"},
	}
	g := newTestGateway(t, accounts, routes)
	now := time.Now().UTC()
	fingerprints := g.state.Load().routeFingerprints
	coldFailure := RequestRecord{Time: now.Format(time.RFC3339Nano), Model: "cold", AccountID: "upstream", Status: 500, Outcome: "uncertain_submission", RouteFingerprint: fingerprints["cold"]}
	g.stats.Record(coldFailure)
	g.stats.RecordRouteObservation(coldFailure)
	for index := 0; index < 600; index++ {
		record := RequestRecord{
			Time:  now.Add(time.Duration(index+1) * time.Nanosecond).Format(time.RFC3339Nano),
			Model: "hot", AccountID: "upstream", Status: 200, Outcome: "success", RouteFingerprint: fingerprints["hot"],
		}
		g.stats.Record(record)
		g.stats.RecordRouteObservation(record)
	}
	for _, record := range g.Stats().Recent {
		if record.Model == "cold" {
			t.Fatal("test fixture did not evict the cold route from Recent")
		}
	}
	ready := httptest.NewRecorder()
	g.serveReadiness(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable || !strings.Contains(ready.Body.String(), `"failed_routes":["cold"]`) {
		t.Fatalf("evicted route failure became falsely ready: status=%d body=%s", ready.Code, ready.Body.String())
	}
}

func TestReadinessUsesLatestRouteObservationAcrossOperations(t *testing.T) {
	accounts := []config.Account{{
		ID: "upstream", Type: "openai", BaseURL: "https://api.example.com/v1", APIKey: "test",
		Models: []string{"chat"}, Concurrency: 4, Weight: 1, Enabled: true,
	}}
	g := newTestGateway(t, accounts, map[string]config.Route{"chat": {Accounts: []string{"upstream"}, UpstreamModel: "chat"}})
	fingerprint := g.state.Load().routeFingerprints["chat"]
	now := time.Now().UTC()
	g.stats.RecordRouteObservation(RequestRecord{
		Time: now.Add(-10 * time.Minute).Format(time.RFC3339Nano), Model: "chat", AccountID: "upstream",
		Operation: config.OperationEmbeddings, Status: http.StatusOK, Outcome: "success", RouteFingerprint: fingerprint,
	})
	g.stats.RecordRouteObservation(RequestRecord{
		Time: now.Format(time.RFC3339Nano), Model: "chat", AccountID: "upstream",
		Operation: config.OperationOpenAIChat, Status: http.StatusOK, Outcome: "success", RouteFingerprint: fingerprint,
	})
	ready := httptest.NewRecorder()
	g.serveReadiness(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || strings.Contains(ready.Body.String(), `"stale_routes":["chat"]`) {
		t.Fatalf("historical low-volume operation became a permanent readiness obligation: status=%d body=%s", ready.Code, ready.Body.String())
	}
}
