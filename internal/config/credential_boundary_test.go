package config

import (
	"strings"
	"testing"
)

func TestConfiguredCredentialBoundariesRejectUnusableOrSharedRoles(t *testing.T) {
	for _, tt := range []struct{ name, key, admin string }{
		{"shared roles", "same-token", "same-token"},
		{"too long for authentication", strings.Repeat("x", 513), "admin"},
		{"reserved managed namespace", "sk-l2a-0123456789abcdef.some-imported-secret", "admin"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Defaults()
			cfg.Server.APIKeyEnv = "ROUND2_TEST_GATEWAY_KEYS"
			cfg.Server.AdminTokenEnv = "ROUND2_TEST_ADMIN_TOKEN"
			t.Setenv(cfg.Server.APIKeyEnv, "")
			t.Setenv(cfg.Server.AdminTokenEnv, "")
			cfg.Server.APIKeys = []string{tt.key}
			cfg.Server.AdminToken = tt.admin
			if err := cfg.Validate(); err == nil || strings.Contains(err.Error(), tt.key) {
				t.Fatalf("invalid credential boundary was accepted or leaked its secret: %v", err)
			}
		})
	}
}
