package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	snapshot := buildOperationsSnapshot(now, cfg, []AccountSnapshot{{ID: "upstream", Enabled: true, Concurrency: 4}}, StatsSnapshot{Recent: []RequestRecord{record}})
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
	snapshot := buildOperationsSnapshot(now, cfg, []AccountSnapshot{{ID: "upstream", Enabled: true, Concurrency: 4}}, StatsSnapshot{Recent: records})
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
	snapshot := buildOperationsSnapshot(now, cfg, []AccountSnapshot{{ID: "upstream", Enabled: true, Concurrency: 4}}, StatsSnapshot{Recent: []RequestRecord{record}})
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
	snapshot := buildOperationsSnapshot(now, cfg, []AccountSnapshot{{ID: "upstream", Enabled: true, Concurrency: 4}}, StatsSnapshot{Recent: []RequestRecord{record}})
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
