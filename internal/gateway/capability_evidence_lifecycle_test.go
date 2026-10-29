package gateway

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestUnchangedCatalogDoesNotReloadOrEraseCallEvidence(t *testing.T) {
	for _, adapter := range []string{"generic-openai", "cli-proxy-api"} {
		t.Run(adapter, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/models" {
					_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"ok","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer upstream.Close()
			account := config.Account{ID: "source", Type: "openai", AdapterID: adapter, BaseURL: upstream.URL + "/v1", APIKey: "fixture-key", Models: []string{"m"}, Enabled: true, Concurrency: 1}
			g := newTestGateway(t, []config.Account{account}, map[string]config.Route{"alias": {Targets: []config.RouteTarget{{Account: "source", Model: "m"}}}})
			if err := g.syncDiscoveredCapabilities(context.Background()); err != nil {
				t.Fatal(err)
			}
			before := g.state.Load()
			if len(before.cfg.Accounts[0].Capabilities) != 1 || before.cfg.Accounts[0].Capabilities[0].Evidence.ObservedAt == "" {
				t.Fatal("first declaration observation was not recorded")
			}
			persisted, err := os.ReadFile(g.configPath)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			g.ServeGateway(w, gatewayRequest(`{"model":"alias","messages":[{"role":"user","content":"ping"}]}`))
			if w.Code != http.StatusOK || len(g.Stats().RouteLatest) != 1 || g.Stats().RouteLatest[0].Outcome != "success" {
				t.Fatalf("no completed-call evidence: status=%d stats=%+v", w.Code, g.Stats().RouteLatest)
			}
			if err := g.syncDiscoveredCapabilities(context.Background()); err != nil {
				t.Fatal(err)
			}
			if g.state.Load() != before {
				t.Fatal("unchanged catalog rebuilt the runtime generation")
			}
			after, err := os.ReadFile(g.configPath)
			if err != nil || !bytes.Equal(persisted, after) {
				t.Fatalf("unchanged catalog rewrote persisted evidence: %v", err)
			}
			if len(g.Stats().RouteLatest) != 1 || g.Stats().RouteLatest[0].Outcome != "success" {
				t.Fatal("unchanged declaration erased a completed logical call")
			}
		})
	}
}

func TestRouteFingerprintIgnoresEvidenceMetadataAndKeepsSemantics(t *testing.T) {
	cfg := schedulerConfig()
	cfg.Accounts = cfg.Accounts[:1]
	cfg.Accounts[0].Capabilities = []config.ChannelCapability{{Model: "m", UpstreamModel: "m", ReasoningEfforts: []string{"auto"},
		Evidence: config.CapabilityEvidence{Status: "declared", Source: "catalog-first", ObservedAt: "2026-10-28T01:00:00Z"},
		Features: map[string]config.CapabilityEvidence{"openai.chat:tools": {Status: "verified", Source: "client-first", ObservedAt: "2026-10-28T01:00:00Z"}},
	}}
	cfg.Routes["alias"] = config.Route{Targets: []config.RouteTarget{{Account: "a", Model: "m"}}}
	baseline := buildRouteFingerprints(cfg)["alias"]
	cfg.Accounts[0].Capabilities[0].Evidence.Source = "catalog-refresh"
	cfg.Accounts[0].Capabilities[0].Evidence.ObservedAt = "2026-10-28T02:00:00Z"
	cfg.Accounts[0].Capabilities[0].Features["openai.chat:tools"] = config.CapabilityEvidence{Status: "verified", Source: "client-refresh", ObservedAt: "2026-10-28T02:00:00Z"}
	if got := buildRouteFingerprints(cfg)["alias"]; got != baseline {
		t.Fatal("source/time annotation invalidated routing evidence")
	}
	for _, item := range []struct {
		name   string
		change func(*config.ChannelCapability)
	}{
		{"capability status", func(c *config.ChannelCapability) { c.Evidence.Status = "unsupported" }},
		{"feature status", func(c *config.ChannelCapability) {
			c.Features["openai.chat:tools"] = config.CapabilityEvidence{Status: "unsupported"}
		}},
		{"model mapping", func(c *config.ChannelCapability) { c.UpstreamModel = "other" }},
		{"reasoning contract", func(c *config.ChannelCapability) { c.ReasoningEfforts = []string{"high"} }},
	} {
		t.Run(item.name, func(t *testing.T) {
			changed := cfg
			changed.Accounts = []config.Account{cloneAccount(cfg.Accounts[0])}
			item.change(&changed.Accounts[0].Capabilities[0])
			if buildRouteFingerprints(changed)["alias"] == baseline {
				t.Fatal("semantic change retained the old call evidence generation")
			}
		})
	}
}
