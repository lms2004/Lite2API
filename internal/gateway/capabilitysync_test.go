package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestDecodeDiscoveredModelIDsAcceptsCommonShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"openai", `{"data":[{"id":"gpt-5.6-sol"},{"id":"fast"}]}`, []string{"gpt-5.6-sol", "fast"}},
		{"models", `{"models":["a",{"id":"b"},"a"]}`, []string{"a", "b"}},
		{"direct", `["x",{"id":"y"}]`, []string{"x", "y"}},
		{"codex-rich", `{"models":[{"slug":"gpt-5.6-sol"}]}`, []string{"gpt-5.6-sol"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeDiscoveredModelIDs([]byte(tc.body)); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDecodeDiscoveredModelsReadsCodexCapabilityMetadata(t *testing.T) {
	models := decodeDiscoveredModels([]byte(`{
		"models":[{
			"slug":"gpt-5.6-sol",
			"supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"xhigh"},{"effort":"max"}],
			"service_tiers":[{"id":"priority","name":"Fast"}],
			"additional_speed_tiers":["fast"]
		}]
	}`))
	if len(models) != 1 {
		t.Fatalf("models=%+v", models)
	}
	got := models[0]
	if got.ID != "gpt-5.6-sol" {
		t.Fatalf("id=%q", got.ID)
	}
	if !reflect.DeepEqual(got.ReasoningEfforts, []string{"low", "medium", "xhigh", "max"}) {
		t.Fatalf("reasoning=%v", got.ReasoningEfforts)
	}
	if !reflect.DeepEqual(got.ServiceTiers, []string{"priority", "fast"}) {
		t.Fatalf("tiers=%v", got.ServiceTiers)
	}
}

func TestDiscoverModelsForAccountUsesAccountAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization=%q", got)
		}
		if got := r.Header.Get("X-Test"); got != "present" {
			t.Errorf("custom header=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.6-sol"},{"id":"gpt-5.6-terra"}]}`))
	}))
	defer server.Close()

	account := config.Account{
		ID: "test", Type: "openai", BaseURL: server.URL + "/v1",
		APIKey: "secret", AuthHeader: "authorization", AuthScheme: "Bearer",
		Headers: map[string]string{"X-Test": "present"}, Enabled: true,
	}
	models, err := discoverModelsForAccount(context.Background(), server.Client(), account)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(discoveredModelIDs(models), []string{"gpt-5.6-sol", "gpt-5.6-terra"}) {
		t.Fatalf("models=%v", models)
	}
}

func TestDiscoverCLIProxyRequestsRichCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("client_version") == "" {
			t.Errorf("expected client_version query for rich CLIProxy catalog")
		}
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.6-sol","supported_reasoning_levels":[{"effort":"low"}],"service_tiers":[{"id":"priority"}]}]}`))
	}))
	defer server.Close()
	account := config.Account{ID: "cliproxy-codex", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: server.URL + "/v1", Enabled: true, AuthHeader: "none"}
	models, err := discoverModelsForAccount(context.Background(), server.Client(), account)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.6-sol" || !reflect.DeepEqual(models[0].ServiceTiers, []string{"priority"}) {
		t.Fatalf("models=%+v", models)
	}
}

func TestMergeSyncedCapabilitiesEnrichesExistingReasoning(t *testing.T) {
	existing := []config.ChannelCapability{{
		Model: "sol", UpstreamModel: "gpt-5.6-sol", ReasoningEfforts: []string{"auto", "high"},
	}}
	inferred := []config.ChannelCapability{{
		Model: "sol", UpstreamModel: "gpt-5.6-sol", ReasoningEfforts: []string{"auto", "none", "low", "medium", "high", "xhigh", "max"},
	}}
	got := mergeSyncedCapabilities(existing, inferred)
	if len(got) != 1 {
		t.Fatalf("capabilities=%+v", got)
	}
	for _, effort := range []string{"none", "low", "medium", "high", "xhigh", "max"} {
		found := false
		for _, value := range got[0].ReasoningEfforts {
			if value == effort {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing effort %q in %v", effort, got[0].ReasoningEfforts)
		}
	}
}

func TestReconcileDiscoveredCapabilitiesMigratesStaleLogicalName(t *testing.T) {
	existing := []config.ChannelCapability{{
		Model: "gpt-oss-120b", UpstreamModel: "gpt-5.3-codex-spark", ReasoningEfforts: []string{"auto", "medium"},
	}}
	inferred := []config.ChannelCapability{{
		Model: "gpt-5.3-codex-spark", UpstreamModel: "gpt-5.3-codex-spark", ReasoningEfforts: []string{"auto"},
	}}
	got, migrations := reconcileDiscoveredCapabilities(existing, inferred)
	if len(got) != 1 || got[0].Model != "gpt-5.3-codex-spark" || got[0].UpstreamModel != "gpt-5.3-codex-spark" {
		t.Fatalf("reconciled capabilities=%+v", got)
	}
	if !containsGatewayCapabilityEffort(got[0].ReasoningEfforts, "medium") {
		t.Fatalf("reconciliation dropped existing reasoning levels: %+v", got[0].ReasoningEfforts)
	}
	if len(migrations) != 1 || migrations[0].From != "gpt-oss-120b" || migrations[0].To != "gpt-5.3-codex-spark" {
		t.Fatalf("migrations=%+v", migrations)
	}
}

func TestApplyDiscoveredCapabilityMigrationsKeepsRouteAlias(t *testing.T) {
	cfg := config.Config{
		Accounts: []config.Account{{
			ID: "cliproxy-codex", Capabilities: []config.ChannelCapability{{
				Model: "gpt-5.3-codex-spark", UpstreamModel: "gpt-5.3-codex-spark", ReasoningEfforts: []string{"auto", "medium"},
			}}, Models: []string{"gpt-5.3-codex-spark"},
		}},
		Routes: map[string]config.Route{
			"gpt": {Model: "gpt-oss-120b", ReasoningEffort: "medium", Targets: []config.RouteTarget{{Account: "cliproxy-codex"}}},
		},
	}
	applyDiscoveredCapabilityMigrations(&cfg, []discoveredCapabilityMigration{{
		AccountID: "cliproxy-codex", From: "gpt-oss-120b", To: "gpt-5.3-codex-spark",
	}})
	if got := cfg.Routes["gpt"].Model; got != "gpt-5.3-codex-spark" {
		t.Fatalf("route model=%q", got)
	}
}

func containsGatewayCapabilityEffort(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
