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

func TestEnvironmentSecretsWin(t *testing.T) {
	t.Setenv("TEST_UPSTREAM_KEY", "from-env")
	a := Account{APIKey: "from-file", APIKeyEnv: "TEST_UPSTREAM_KEY"}
	if got := a.ResolvedAPIKey(); got != "from-env" {
		t.Fatalf("got %q", got)
	}
}
