package config

import (
	"path/filepath"
	"testing"
)

func TestCapabilityEvidenceValidationAndClone(t *testing.T) {
	capability := ChannelCapability{Model: "m", UpstreamModel: "m", ReasoningEfforts: []string{"auto"}, Features: map[string]CapabilityEvidence{
		"openai.chat:tools": {Status: "verified", Source: "client-fixture", ObservedAt: "2026-10-28T00:00:00Z"},
	}}
	if err := validateCapabilityEvidence(capability); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Accounts: []Account{{Capabilities: []ChannelCapability{capability}}}}
	cloned := cloneConfig(cfg)
	cloned.Accounts[0].Capabilities[0].Features["openai.chat:tools"] = CapabilityEvidence{Status: "unsupported"}
	if cfg.Accounts[0].Capabilities[0].Features["openai.chat:tools"].Status != "verified" {
		t.Fatal("cloning aliases mutable evidence map")
	}
	for _, invalid := range []ChannelCapability{
		{Evidence: CapabilityEvidence{Status: "verified"}},
		{Evidence: CapabilityEvidence{Status: "healthy"}},
		{Features: map[string]CapabilityEvidence{"tools": {Status: "declared"}}},
		{Features: map[string]CapabilityEvidence{"openai.chat:tools": {Status: "verified", Source: "fixture", ObservedAt: "yesterday"}}},
		{UpstreamModel: "m", Features: map[string]CapabilityEvidence{"openai.chat:tools": {Status: "unsupported", UpstreamModel: "other"}}},
		{UpstreamModel: "m", Evidence: CapabilityEvidence{Status: "unsupported", UpstreamModel: "other"}},
	} {
		if validateCapabilityEvidence(invalid) == nil {
			t.Fatalf("invalid evidence accepted: %+v", invalid)
		}
	}
}

func TestVerifiedEvidenceCannotMoveToAnotherSourceOrModel(t *testing.T) {
	cfg := Defaults()
	cfg.Accounts = []Account{{ID: "a", Type: "openai", BaseURL: "https://example.invalid/v1", APIKey: "first-key", Models: []string{"m"}, Enabled: true}}
	cfg = Normalize(cfg)
	cfg.Accounts[0].Capabilities = []ChannelCapability{{Model: "m", UpstreamModel: "m", ReasoningEfforts: []string{"auto"}, Features: map[string]CapabilityEvidence{
		"openai.chat:call": {Status: "verified", Source: "capability_test:fixture", ObservedAt: "2026-10-28T00:00:00Z", SourceIdentity: AccountSourceIdentity(cfg.Accounts[0]), UpstreamModel: "m", ReasoningEffort: "auto", LastCheckOutcome: "passed"},
	}}}
	if Normalize(cfg).Accounts[0].Capabilities[0].Features["openai.chat:call"].Status != "verified" {
		t.Fatal("unchanged scope lost proof")
	}
	for _, item := range []struct {
		name   string
		change func(*Account)
	}{
		{"credential", func(a *Account) { a.APIKey = "other-key" }},
		{"endpoint", func(a *Account) { a.BaseURL = "https://other.invalid/v1" }},
		{"authentication headers", func(a *Account) { a.Headers = map[string]string{"X-Credential": "other"} }},
		{"model mapping", func(a *Account) {
			a.Capabilities[0].UpstreamModel = "other-model"
			a.Models = append(a.Models, "other-model")
		}},
	} {
		t.Run(item.name, func(t *testing.T) {
			changed := cloneConfig(cfg)
			item.change(&changed.Accounts[0])
			path := filepath.Join(t.TempDir(), "config.json")
			if err := NewStore(path).Save(changed); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			fact := loaded.Accounts[0].Capabilities[0].Features["openai.chat:call"]
			if fact.Status != "unknown" || fact.LastCheckOutcome != "inconclusive" {
				t.Fatalf("changed source retained proof: %+v", fact)
			}
		})
	}
	t.Setenv("CAPABILITY_TEST_KEY", "first-env-key")
	cfg.Accounts[0].APIKeyEnv = "CAPABILITY_TEST_KEY"
	fact := cfg.Accounts[0].Capabilities[0].Features["openai.chat:call"]
	fact.SourceIdentity = AccountSourceIdentity(cfg.Accounts[0])
	cfg.Accounts[0].Capabilities[0].Features["openai.chat:call"] = fact
	t.Setenv("CAPABILITY_TEST_KEY", "replacement-env-key")
	if Normalize(cfg).Accounts[0].Capabilities[0].Features["openai.chat:call"].Status != "unknown" {
		t.Fatal("environment credential change retained proof")
	}
}
