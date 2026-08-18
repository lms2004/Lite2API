package config

import "testing"

func TestFilterDiscoveredModelsScopesSharedCLIProxy(t *testing.T) {
	models := []string{
		"gpt-5.6-sol",
		"claude-code/claude-opus-4-6",
		"antigravity/claude-opus-4-6-thinking",
		"gemini-3.6-flash",
	}
	claude := Account{ID: "cliproxy-claude-code", AdapterID: "cli-proxy-api"}
	got := FilterDiscoveredModels(claude, models)
	if len(got) != 1 || got[0] != "claude-code/claude-opus-4-6" {
		t.Fatalf("claude scope=%v", got)
	}
	codex := Account{ID: "cliproxy-codex", AdapterID: "cli-proxy-api"}
	got = FilterDiscoveredModels(codex, models)
	if len(got) != 1 || got[0] != "gpt-5.6-sol" {
		t.Fatalf("codex scope=%v", got)
	}
	aggregate := Account{ID: "cliproxy-oauth", AdapterID: "cli-proxy-api"}
	got = FilterDiscoveredModels(aggregate, models)
	if len(got) != len(models) {
		t.Fatalf("aggregate scope=%v", got)
	}
}

func TestInferDiscoveredCapabilitiesUnifiesProviderSpecificModels(t *testing.T) {
	account := Account{ID: "cliproxy-oauth", AdapterID: "cli-proxy-api"}
	caps := InferDiscoveredCapabilities(account, []string{
		"claude-code/claude-opus-4-6",
		"antigravity/claude-opus-4-6-thinking",
		"gemini-3.6-flash-thinking",
	})
	byModel := make(map[string]ChannelCapability, len(caps))
	for _, capability := range caps {
		byModel[capability.Model] = capability
	}
	if got := byModel["claude-opus-4-6"]; got.Model == "" {
		t.Fatalf("missing unified Claude capability in %+v", caps)
	} else if !containsString(got.ReasoningEfforts, "auto") || !containsString(got.ReasoningEfforts, "high") {
		t.Fatalf("Claude efforts=%v", got.ReasoningEfforts)
	}
	if got := byModel["gemini-3.6-flash"]; got.Model == "" || !containsString(got.ReasoningEfforts, "high") {
		t.Fatalf("Gemini thinking capability=%+v", got)
	}
}

func TestInferCodexCapabilitiesPrefersCanonicalModelID(t *testing.T) {
	caps := InferCodexCapabilities([]string{"gpt-5.6", "sol", "gpt-5.6-sol"})
	if len(caps) != 1 {
		t.Fatalf("capabilities=%+v", caps)
	}
	if caps[0].Model != "sol" || caps[0].UpstreamModel != "gpt-5.6-sol" {
		t.Fatalf("canonical capability=%+v", caps[0])
	}
	for _, effort := range []string{"none", "low", "medium", "high", "xhigh", "max"} {
		if !containsString(caps[0].ReasoningEfforts, effort) {
			t.Fatalf("missing effort %q in %v", effort, caps[0].ReasoningEfforts)
		}
	}
}
