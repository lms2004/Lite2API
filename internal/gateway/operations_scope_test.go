package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func TestOperationsAvailabilityUsesSchedulingBreakerScope(t *testing.T) {
	for _, tc := range []struct {
		name, operation, model, credential string
		available                          bool
	}{
		{"other model", config.OperationOpenAIChat, "other", "0123456789abcdef", true},
		{"other operation", config.OperationOpenAIResponses, "selected", "0123456789abcdef", true},
		{"other credential", config.OperationOpenAIChat, "selected", "fedcba9876543210", true},
		{"same scope", config.OperationOpenAIChat, "selected", "0123456789abcdef", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Accounts = []config.Account{{ID: "source", Type: "openai", APIKey: "fixture-secret", BaseURL: "https://example.invalid", Models: []string{"selected", "other"}, Operations: []string{config.OperationOpenAIChat}, Enabled: true, Concurrency: 1}}
			cfg.Routes = map[string]config.Route{"alias": {Targets: []config.RouteTarget{{Account: "source", Model: "selected", Credential: "0123456789abcdef"}}}}
			s := NewScheduler(cfg)
			account := s.Get("source")
			attempt := account.beginAttempt(tc.operation, tc.model, tc.credential)
			account.reportFailure(attempt, "fixture failure", 1, time.Minute, true)
			now := time.Now()
			accounts := s.Snapshot()
			if accounts[0].CircuitOpenUntil == "" {
				t.Fatal("fixture did not open the aggregate breaker")
			}
			operations := buildOperationsSnapshot(now, cfg, accounts, StatsSnapshot{})
			if got := operations.Routes[0].UnknownTargets == 1; got != tc.available || s.RouteAvailable("alias", now) != tc.available {
				t.Fatalf("health diverged from scoped availability: want=%v snapshot=%+v scheduler=%v", tc.available, operations, s.RouteAvailable("alias", now))
			}
			selection, err := s.Select(context.Background(), "alias", config.OperationOpenAIChat, "", nil, 0)
			if selection != nil {
				selection.Release()
			}
			if (err == nil) != tc.available {
				t.Fatalf("selection disagreed with health: available=%v err=%v", tc.available, err)
			}
		})
	}
}

func TestOperationsLegacyTargetsResolveModelBeforeMatchingObservation(t *testing.T) {
	now := time.Now()
	cfg := config.Defaults()
	cfg.Accounts = []config.Account{{ID: "source", Type: "openai", APIKey: "fixture-secret", Enabled: true, Models: []string{"correct", "wrong"}, ModelMap: map[string]string{"alias": "correct"}, Operations: []string{config.OperationOpenAIChat}}}
	for _, allAccounts := range []bool{false, true} {
		route := config.Route{AllAccounts: allAccounts}
		if !allAccounts {
			route.Accounts = []string{"source"}
		}
		cfg.Routes = map[string]config.Route{"alias": route}
		for _, model := range []string{"", "wrong", "correct"} {
			record := RequestRecord{Time: now.Format(time.RFC3339Nano), Model: "alias", UpstreamModel: model, AccountID: "source", Operation: config.OperationOpenAIChat, Status: 200, Outcome: "success"}
			result := buildOperationsSnapshot(now, cfg, []AccountSnapshot{{ID: "source", Enabled: true}}, StatsSnapshot{RouteLatest: []RequestRecord{record}})
			if got := result.Routes[0].ReadyTargets == 1; got != (model == "correct") {
				t.Fatalf("legacy target accepted another model: all_accounts=%v observed=%q result=%+v", allAccounts, model, result)
			}
		}
	}
}

func TestReadinessRejectsLegacySourceThatCannotSelectModel(t *testing.T) {
	for _, allAccounts := range []bool{false, true} {
		account := config.Account{ID: "source", Type: "openai", APIKey: "fixture-secret", BaseURL: "https://example.invalid", Models: []string{"other"}, Operations: []string{config.OperationOpenAIChat}, Enabled: true}
		route := config.Route{AllAccounts: allAccounts}
		if !allAccounts {
			route.Accounts = []string{"source"}
		}
		g := newTestGateway(t, []config.Account{account}, map[string]config.Route{"missing": route})
		w := httptest.NewRecorder()
		g.serveReadiness(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("unselectable legacy model reported available: all_accounts=%v status=%d body=%s", allAccounts, w.Code, w.Body.String())
		}
	}
}

func TestOperationsReadinessHonorsDeclaredInterfaceExclusions(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		cfg := config.Defaults()
		cfg.Accounts = []config.Account{{ID: "source", Type: "openai", Enabled: true, Models: []string{"m"}, Operations: []string{config.OperationOpenAIChat}, Capabilities: []config.ChannelCapability{{Model: "m", UpstreamModel: "m", Features: map[string]config.CapabilityEvidence{"openai.chat:call": {Status: "unsupported"}}}}}}
		route := config.Route{Accounts: []string{"source"}, UpstreamModel: "m"}
		if explicit {
			route = config.Route{Targets: []config.RouteTarget{{Account: "source", Model: "m"}}}
		}
		cfg.Routes = map[string]config.Route{"alias": route}
		s := NewScheduler(cfg)
		now := time.Now()
		if result := buildOperationsSnapshot(now, cfg, s.Snapshot(), StatsSnapshot{}); result.State != HealthUnavailable || s.RouteAvailable("alias", now) {
			t.Fatalf("excluded interface reported available: explicit=%v snapshot=%+v", explicit, result)
		}
		if selection, err := s.Select(context.Background(), "alias", config.OperationOpenAIChat, "", nil, 0); selection != nil || err != ErrNoEligibleAccount {
			if selection != nil {
				selection.Release()
			}
			t.Fatalf("selection disagreed with availability: selection=%v err=%v", selection, err)
		}
	}
}

func TestOperationsDoesNotRenewSuccessThroughAnotherAvailableInterface(t *testing.T) {
	cfg := config.Defaults()
	cfg.Accounts = []config.Account{{ID: "source", Type: "openai", Enabled: true, Models: []string{"m"}, Operations: []string{config.OperationOpenAIChat, config.OperationOpenAIResponses}}}
	cfg.Routes = map[string]config.Route{"alias": {Accounts: []string{"source"}, UpstreamModel: "m"}}
	s := NewScheduler(cfg)
	account := s.Get("source")
	account.reportFailure(account.beginAttempt(config.OperationOpenAIChat, "m"), "fixture failure from another alias", 1, time.Minute, true)
	now := time.Now()
	for _, operation := range []string{config.OperationOpenAIChat, config.OperationOpenAIResponses} {
		record := RequestRecord{Time: now.Add(-time.Second).Format(time.RFC3339Nano), Model: "alias", UpstreamModel: "m", AccountID: "source", Operation: operation, Status: 200, Outcome: "success"}
		result := buildOperationsSnapshot(now, cfg, s.Snapshot(), StatsSnapshot{RouteLatest: []RequestRecord{record}})
		want := HealthDegraded
		if operation == config.OperationOpenAIResponses {
			want = HealthReady
		}
		if result.Routes[0].State != want {
			t.Fatalf("observation operation=%s want=%s got=%+v", operation, want, result.Routes[0])
		}
	}
}
