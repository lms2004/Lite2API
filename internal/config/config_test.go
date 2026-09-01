package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestNormalizeCanonicalizesRouteStrategy(t *testing.T) {
	cfg := Defaults()
	cfg.Routes["alias"] = Route{Strategy: "  PRIORITY  "}
	cfg = Normalize(cfg)
	if got := cfg.Routes["alias"].Strategy; got != "priority" {
		t.Fatalf("strategy = %q, want priority", got)
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

func TestValidateCredentialPinRequiresCLIProxyAccount(t *testing.T) {
	cfg := Defaults()
	cfg.Server.AllowPrivateHTTPUpstream = true
	cfg.Accounts = []Account{{
		ID: "pool", Type: "openai", AdapterID: "cli-proxy-api", BaseURL: "http://127.0.0.1:1/v1",
		Models: []string{"m"}, Enabled: true,
	}}
	cfg.Routes["m"] = Route{Targets: []RouteTarget{{Account: "pool", Credential: "0123456789abcdef", Model: "m"}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid CLIProxy credential pin: %v", err)
	}

	account := cfg.Accounts[0]
	account.AdapterID = "generic-openai"
	cfg.Accounts[0] = account
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "credential pinning") {
		t.Fatalf("non-CLIProxy credential pin error = %v", err)
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

func TestLoadRejectsUnknownTrailingAndNumericDuration(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want string
	}{
		{name: "unknown field", body: `{"server":{},"accounts":[],"routes":{},"typo":true}`, want: "unknown field"},
		{name: "trailing value", body: `{"server":{},"accounts":[],"routes":{}} {}`, want: "multiple JSON values"},
		{name: "numeric duration", body: `{"server":{"request_read_timeout":30},"accounts":[],"routes":{}}`, want: "duration must be a duration string"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(test.body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestStoreEffectiveDoesNotPersistEnvironmentOverlay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	desired := Defaults()
	desired.Server.AdminAutoLogin = false
	desired.Server.MaxBodyBytes = 2 << 20
	store := NewStore(path)
	if err := store.Save(desired); err != nil {
		t.Fatal(err)
	}

	t.Setenv("LITE2API_ADMIN_AUTO_LOGIN", "true")
	t.Setenv("LITE2API_MAX_BODY_BYTES", "7340032")
	effective, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !effective.Server.AdminAutoLogin || effective.Server.MaxBodyBytes != 7<<20 {
		t.Fatalf("environment overlay was not applied: %+v", effective.Server)
	}
	if err := store.SaveEffective(effective); err != nil {
		t.Fatal(err)
	}

	t.Setenv("LITE2API_ADMIN_AUTO_LOGIN", "")
	t.Setenv("LITE2API_MAX_BODY_BYTES", "")
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Server.AdminAutoLogin || reloaded.Server.MaxBodyBytes != 2<<20 {
		t.Fatalf("environment overlay leaked into desired config: %+v", reloaded.Server)
	}
}

func TestNormalizeDeepCopiesMutableConfiguration(t *testing.T) {
	cfg := Defaults()
	cfg.Accounts = []Account{{
		ID: "a", Type: "openai", BaseURL: "https://api.example.com/v1",
		Headers: map[string]string{"X-Test": "original"}, Models: []string{"m"},
		Capabilities: []ChannelCapability{{Model: "m", UpstreamModel: "m", ReasoningEfforts: []string{"auto"}}},
	}}
	cfg.Routes["m"] = Route{Targets: []RouteTarget{{Account: "a", Model: "m"}}}
	normalized := Normalize(cfg)
	normalized.Accounts[0].Headers["X-Test"] = "changed"
	normalized.Accounts[0].Models[0] = "changed"
	normalized.Accounts[0].Capabilities[0].ReasoningEfforts[0] = "high"
	route := normalized.Routes["m"]
	route.Targets[0].Model = "changed"
	normalized.Routes["m"] = route
	if cfg.Accounts[0].Headers["X-Test"] != "original" || cfg.Accounts[0].Models[0] != "m" ||
		cfg.Accounts[0].Capabilities[0].ReasoningEfforts[0] != "auto" || cfg.Routes["m"].Targets[0].Model != "m" {
		t.Fatalf("Normalize mutated or retained aliases to the source: source=%+v normalized=%+v", cfg, normalized)
	}
}

func TestValidateRequiresUnambiguousRouteSchema(t *testing.T) {
	base := Defaults()
	base.Accounts = []Account{{ID: "a", Type: "openai", BaseURL: "https://api.example.com/v1", Models: []string{"m"}, Enabled: true}}

	mixed := cloneConfig(base)
	mixed.Routes["alias"] = Route{Accounts: []string{"a"}, Targets: []RouteTarget{{Account: "a", Model: "m"}}}
	if err := mixed.Validate(); err == nil || !strings.Contains(err.Error(), "cannot combine") {
		t.Fatalf("mixed route error=%v", err)
	}
	empty := cloneConfig(base)
	empty.Routes["alias"] = Route{}
	if err := empty.Validate(); err == nil || !strings.Contains(err.Error(), "no targets") {
		t.Fatalf("empty route error=%v", err)
	}
	wildcard := cloneConfig(base)
	wildcard.Routes["alias"] = Route{AllAccounts: true}
	if err := wildcard.Validate(); err != nil {
		t.Fatalf("explicit wildcard route: %v", err)
	}
	unsupported := cloneConfig(base)
	unsupported.Routes["alias"] = Route{Accounts: []string{"a"}, UpstreamModel: "other"}
	if err := unsupported.Validate(); err == nil || !strings.Contains(err.Error(), "does not advertise") {
		t.Fatalf("unsupported upstream error=%v", err)
	}
}

func TestStoreRejectsConfigurationDataPathCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Defaults()
	cfg.Server.ClientKeysPath = filepath.Base(path)
	if err := NewStore(path).Save(cfg); err == nil || !strings.Contains(err.Error(), "different files") {
		t.Fatalf("collision error=%v", err)
	}
}

func TestLoadRejectsConfigurationDataPathCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{"version":1,"server":{"client_keys_path":"config.json"}}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "different files") {
		t.Fatalf("startup collision error=%v", err)
	}
}

func TestValidateRejectsUnboundedResourceConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "body", mutate: func(cfg *Config) { cfg.Server.MaxBodyBytes = 257 << 20 }},
		{name: "inflight", mutate: func(cfg *Config) { cfg.Server.MaxInFlightRequests = 65537 }},
		{name: "stream timeout", mutate: func(cfg *Config) { cfg.Server.StreamIdleTimeout = Duration{25 * time.Hour} }},
		{name: "failover", mutate: func(cfg *Config) { cfg.Server.MaxFailoverAttempts = 65 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Defaults()
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("expected unsafe %s configuration to be rejected", test.name)
			}
		})
	}
}

func TestValidateEnforcesRoutingIdentifierBudgets(t *testing.T) {
	base := Defaults()
	base.Accounts = []Account{{
		ID: "account", Type: "openai", BaseURL: "https://api.example.com/v1",
		Models: []string{"m"}, Enabled: true,
	}}
	base.Routes["m"] = Route{Accounts: []string{"account"}}
	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "account id", mutate: func(cfg *Config) {
			cfg.Accounts[0].ID = strings.Repeat("a", MaxAccountIDBytes+1)
		}},
		{name: "account model", mutate: func(cfg *Config) {
			cfg.Accounts[0].Models[0] = strings.Repeat("m", MaxModelIDBytes+1)
		}},
		{name: "route alias", mutate: func(cfg *Config) {
			delete(cfg.Routes, "m")
			cfg.Routes[strings.Repeat("r", MaxModelIDBytes+1)] = Route{Accounts: []string{"account"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := cloneConfig(base)
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("expected oversized %s to be rejected", test.name)
			}
		})
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
