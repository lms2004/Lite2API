package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRejectsUnsafeHTTP(t *testing.T) {
	cfg := Defaults()
	cfg.Accounts = []Account{{ID: "remote", Type: "openai", BaseURL: "http://192.168.10.10/v1", Enabled: true}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unsafe HTTP URL to be rejected")
	}
	cfg.Server.AllowPrivateHTTPUpstream = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("allow HTTP: %v", err)
	}
}

func TestStoreUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	cfg := Defaults()
	cfg.Server.APIKeys = []string{"secret"}
	if err := NewStore(path).Save(cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

func TestAdminAutoLoginEnvironmentIsExplicit(t *testing.T) {
	if Normalize(Defaults()).Server.AdminAutoLogin {
		t.Fatal("admin auto login must default to disabled")
	}
	t.Setenv("LITE2API_ADMIN_AUTO_LOGIN", "true")
	if !Normalize(Defaults()).Server.AdminAutoLogin {
		t.Fatal("admin auto login environment override was not applied")
	}
}

func TestEnvironmentSecretsWin(t *testing.T) {
	t.Setenv("TEST_UPSTREAM_KEY", "from-env")
	a := Account{APIKey: "from-file", APIKeyEnv: "TEST_UPSTREAM_KEY"}
	if got := a.ResolvedAPIKey(); got != "from-env" {
		t.Fatalf("got %q", got)
	}
}

func TestValidateOrderedRouteTargets(t *testing.T) {
	cfg := Defaults()
	cfg.Server.AllowPrivateHTTPUpstream = true
	cfg.Accounts = []Account{{
		ID: "channel-a", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", Enabled: true,
		Models: []string{"gpt"}, ModelMap: map[string]string{"gpt": "gpt-real"},
	}}
	cfg.Routes["gpt"] = Route{Targets: []RouteTarget{{Account: "channel-a", Model: "gpt-real", ReasoningEffort: "high"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid target chain: %v", err)
	}
	route := cfg.Routes["gpt"]
	route.Targets[0].Model = "not-advertised"
	cfg.Routes["gpt"] = route
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an incompatible target model to be rejected")
	}
}

func TestValidateLogicalRouteAgainstRealChannelCapabilities(t *testing.T) {
	cfg := Defaults()
	cfg.Server.AllowPrivateHTTPUpstream = true
	cfg.Accounts = []Account{
		{
			ID: "antigravity", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", Enabled: true,
			Models: []string{"antigravity/claude-opus-4-6-thinking"},
			Capabilities: []ChannelCapability{{
				Model: "claude-opus-4-6", UpstreamModel: "antigravity/claude-opus-4-6-thinking", ReasoningEfforts: []string{"high"},
			}},
		},
		{
			ID: "claude-code", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", Enabled: true,
			Models: []string{"claude-code/claude-opus-4-6"},
			Capabilities: []ChannelCapability{{
				Model: "claude-opus-4-6", UpstreamModel: "claude-code/claude-opus-4-6", ReasoningEfforts: []string{"low", "high"},
			}},
		},
	}
	cfg.Routes["claude"] = Route{
		Model: "claude-opus-4-6", ReasoningEffort: "high",
		Targets: []RouteTarget{{Account: "antigravity"}, {Account: "claude-code"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("high route should use both channels: %v", err)
	}
	route := cfg.Routes["claude"]
	route.ReasoningEffort = "low"
	cfg.Routes["claude"] = route
	if err := cfg.Validate(); err == nil {
		t.Fatal("low route should reject antigravity when only claude-code supports it")
	}
	route.Targets = []RouteTarget{{Account: "claude-code"}}
	cfg.Routes["claude"] = route
	if err := cfg.Validate(); err != nil {
		t.Fatalf("low route through claude-code: %v", err)
	}
}

func TestInferCodexCapabilitiesSeparatesModelAndReasoningEffort(t *testing.T) {
	capabilities := InferCodexCapabilities([]string{
		"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6", "fast", "claude-test", "gpt-test", "*",
	})
	if len(capabilities) != 3 {
		t.Fatalf("capabilities=%+v, want three concrete routable Codex models", capabilities)
	}

	byModel := make(map[string]ChannelCapability, len(capabilities))
	for _, capability := range capabilities {
		byModel[capability.Model] = capability
	}
	for _, model := range []string{"luna", "sol", "gpt-test"} {
		if _, ok := byModel[model]; !ok {
			t.Fatalf("missing logical model %q in %+v", model, capabilities)
		}
	}
	if _, ok := byModel["fast"]; ok {
		t.Fatalf("Fast must be an execution profile, not a concrete model: %+v", capabilities)
	}
	if got := byModel["sol"].UpstreamModel; got != "gpt-5.6-sol" {
		t.Fatalf("sol upstream model=%q", got)
	}
	if !containsString(byModel["sol"].ReasoningEfforts, "max") || !containsString(byModel["luna"].ReasoningEfforts, "max") {
		t.Fatalf("GPT-5.6 capabilities must support max: %+v", byModel)
	}
	if _, ok := byModel["claude-test"]; ok {
		t.Fatal("non-Codex model was inferred as a Codex capability")
	}
}

func TestNormalizeBackfillsCLIProxyCapabilities(t *testing.T) {
	cfg := Normalize(Config{
		Server: Defaults().Server,
		Accounts: []Account{{
			ID: "cliproxy-oauth", Type: "openai", AdapterID: "cli-proxy-api",
			BaseURL: "http://127.0.0.1:45682/v1", Models: []string{"gpt-5.6-sol"}, Enabled: true,
		}},
		Routes: map[string]Route{},
	})
	if len(cfg.Accounts) != 1 || len(cfg.Accounts[0].Capabilities) != 1 {
		t.Fatalf("normalized account capabilities=%+v", cfg.Accounts)
	}
	capability := cfg.Accounts[0].Capabilities[0]
	if capability.Model != "sol" || capability.UpstreamModel != "gpt-5.6-sol" || !containsString(capability.ReasoningEfforts, "max") {
		t.Fatalf("unexpected normalized capability=%+v", capability)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
