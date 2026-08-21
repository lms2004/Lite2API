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
			Accounts: []string{"first", "second"},
			Targets:  []config.RouteTarget{{Account: "first", Model: "m"}, {Account: "second", Model: "m"}},
		},
		"secondary": {Accounts: []string{"second"}},
	})

	if err := g.DeleteAccount("first"); err != nil {
		t.Fatal(err)
	}

	cfg := g.Config()
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].ID != "second" {
		t.Fatalf("accounts after delete=%+v", cfg.Accounts)
	}
	chat := cfg.Routes["chat"]
	if len(chat.Accounts) != 1 || chat.Accounts[0] != "second" || len(chat.Targets) != 1 || chat.Targets[0].Account != "second" {
		t.Fatalf("chat route after delete=%+v", chat)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("deleted config is invalid: %v", err)
	}
}
