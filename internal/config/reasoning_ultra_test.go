package config

import "testing"

func TestValidReasoningEffortAcceptsUltra(t *testing.T) {
	if !ValidReasoningEffort("ultra") {
		t.Fatal("ultra reasoning returned by rich upstream catalogs must be routable")
	}
}

func TestRichCatalogPreservesUltraReasoning(t *testing.T) {
	account := Account{ID: "cliproxy-codex", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: "http://127.0.0.1:8317/v1"}
	caps := InferDiscoveredModelCapabilities(account, []DiscoveredModel{{
		ID:               "gpt-5.6-sol",
		ReasoningEfforts: []string{"low", "high", "ultra"},
	}})
	for _, capability := range caps {
		if capability.Model != "sol" {
			continue
		}
		if !containsString(capability.ReasoningEfforts, "ultra") {
			t.Fatalf("rich capability dropped ultra: %v", capability.ReasoningEfforts)
		}
		return
	}
	t.Fatalf("missing sol capability: %+v", caps)
}
