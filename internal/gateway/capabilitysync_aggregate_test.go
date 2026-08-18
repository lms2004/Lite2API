package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestAggregateCLIProxyKeepsFullCatalogAndAddsRichCodexMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("client_version") != "" {
			_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.6-sol","supported_reasoning_levels":[{"effort":"low"},{"effort":"ultra"}],"service_tiers":[{"id":"priority"}]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.6-sol"},{"id":"claude-code/claude-opus-4-6"},{"id":"gemini-3.6-flash"}]}`))
	}))
	defer server.Close()

	account := config.Account{
		ID: "cliproxy-oauth", Type: "openai", AdapterID: "cli-proxy-api",
		BaseURL: server.URL + "/v1", Enabled: true, AuthHeader: "none",
	}
	catalog, err := discoverModelsForAccount(context.Background(), server.Client(), account)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 3 {
		t.Fatalf("aggregate catalog lost provider models: %+v", catalog)
	}
	var codex config.DiscoveredModel
	for _, model := range catalog {
		if model.ID == "gpt-5.6-sol" {
			codex = model
		}
	}
	if codex.ID == "" || !containsString(codex.ReasoningEfforts, "ultra") || !containsString(codex.ServiceTiers, "priority") {
		t.Fatalf("rich Codex supplement not merged: %+v", codex)
	}

	caps := config.InferDiscoveredModelCapabilities(account, catalog)
	if !hasGatewayCapability(caps, "claude-opus-4-6") || !hasGatewayCapability(caps, "gemini-3.6-flash") || !hasGatewayCapability(caps, "sol@fast") {
		t.Fatalf("aggregate capabilities incomplete: %+v", caps)
	}
}

func hasGatewayCapability(caps []config.ChannelCapability, model string) bool {
	for _, capability := range caps {
		if capability.Model == model {
			return true
		}
	}
	return false
}
