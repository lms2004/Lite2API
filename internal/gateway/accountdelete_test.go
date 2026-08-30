package gateway

import (
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestDeleteAccountRemovesRouteReferences(t *testing.T) {
	g := newTestGateway(t, []config.Account{
		{ID: "first", Type: "openai", BaseURL: "https://first.example.com/v1", APIKey: "first-secret", Models: []string{"m"}, Enabled: true, Weight: 1},
		{ID: "second", Type: "openai", BaseURL: "https://second.example.com/v1", APIKey: "second-secret", Models: []string{"m"}, Enabled: true, Weight: 1},
	}, map[string]config.Route{
		"chat": {
			Targets: []config.RouteTarget{{Account: "first", Model: "m"}, {Account: "second", Model: "m"}},
		},
		"legacy":    {Accounts: []string{"first", "second"}},
		"secondary": {Accounts: []string{"second"}, UpstreamModel: "m"},
	})

	if err := g.DeleteAccount("first"); err != nil {
		t.Fatal(err)
	}

	cfg := g.Config()
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].ID != "second" {
		t.Fatalf("accounts after delete=%+v", cfg.Accounts)
	}
	chat := cfg.Routes["chat"]
	if len(chat.Accounts) != 0 || len(chat.Targets) != 1 || chat.Targets[0].Account != "second" {
		t.Fatalf("chat route after delete=%+v", chat)
	}
	legacy := cfg.Routes["legacy"]
	if len(legacy.Accounts) != 1 || legacy.Accounts[0] != "second" {
		t.Fatalf("legacy route after delete=%+v", legacy)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("deleted config is invalid: %v", err)
	}
}

func TestDeleteLastRouteAccountRemovesRouteInsteadOfCreatingWildcard(t *testing.T) {
	g := newTestGateway(t, []config.Account{
		{ID: "first", Type: "openai", BaseURL: "https://first.example.com/v1", APIKey: "first-secret", Models: []string{"m"}, Enabled: true, Weight: 1},
		{ID: "unrelated", Type: "openai", BaseURL: "https://other.example.com/v1", APIKey: "other-secret", Models: []string{"m"}, Enabled: true, Weight: 1},
	}, map[string]config.Route{
		"legacy": {Accounts: []string{"first"}},
		"target": {Targets: []config.RouteTarget{{Account: "first", Model: "m"}}},
	})

	if err := g.DeleteAccount("first"); err != nil {
		t.Fatal(err)
	}
	if _, exists := g.Config().Routes["legacy"]; exists {
		t.Fatal("legacy route survived without an account and would become a wildcard")
	}
	if _, exists := g.Config().Routes["target"]; exists {
		t.Fatal("target route survived without a target and would become a wildcard")
	}
}
