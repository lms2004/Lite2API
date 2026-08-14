package gateway

import (
	"strings"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestAdapterCatalogAssociatesConfiguredAccounts(t *testing.T) {
	catalog := AdapterCatalog([]config.Account{{ID: "grok-local", BaseURL: "http://127.0.0.1:45680/v1"}})
	if len(catalog) < 20 {
		t.Fatalf("catalog contains %d entries, want broad adapter coverage", len(catalog))
	}
	for _, adapter := range catalog {
		if adapter.ID == "grok2api" {
			if adapter.Status != "configured" || len(adapter.AccountIDs) != 1 || adapter.AccountIDs[0] != "grok-local" {
				t.Fatalf("grok adapter=%+v", adapter)
			}
			return
		}
	}
	t.Fatal("grok2api adapter is missing")
}

func TestExportAccountsSelectedAndProxyRoundTrip(t *testing.T) {
	enabled := true
	cfg := config.Normalize(config.Config{
		Server: config.Defaults().Server,
		Accounts: []config.Account{
			{ID: "one", Name: "One", Type: "openai", BaseURL: "https://api.example.com/v1", APIKey: "secret", Headers: map[string]string{"X-Secret": "header"}, Models: []string{"m"}, ModelMap: map[string]string{"alias": "m"}, Enabled: enabled, ProxyURL: "socks5://user:pass@127.0.0.1:1080"},
			{ID: "two", Name: "Two", Type: "openai", BaseURL: "https://other.example.com/v1", APIKeyEnv: "OTHER_KEY", Enabled: enabled},
		},
		Routes: map[string]config.Route{},
	})
	data, err := ExportAccounts(cfg, AccountExportRequest{IDs: []string{"one"}, IncludeProxies: true})
	if err != nil {
		t.Fatal(err)
	}
	if data.Type != "lite2api-data" || data.Version != 1 || len(data.Accounts) != 1 || len(data.Proxies) != 1 {
		t.Fatalf("data=%+v", data)
	}
	if data.Accounts[0].APIKey != "secret" || data.Accounts[0].Headers["X-Secret"] != "header" || data.Accounts[0].ProxyKey == nil {
		t.Fatalf("account=%+v", data.Accounts[0])
	}

	g := newTestGateway(t, nil, nil)
	result, err := g.ImportAccounts(AccountImportRequest{Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProxyCreated != 1 || result.AccountCreated != 1 || g.Config().Accounts[0].ProxyURL != "socks5://user:pass@127.0.0.1:1080" {
		t.Fatalf("result=%+v account=%+v", result, g.Config().Accounts[0])
	}
}

func TestExportAccountsOmitsProxyUnlessRequested(t *testing.T) {
	cfg := config.Normalize(config.Config{Server: config.Defaults().Server, Accounts: []config.Account{{ID: "one", Type: "openai", BaseURL: "https://api.example.com/v1", APIKey: "secret", Enabled: true, ProxyURL: "http://127.0.0.1:8080"}}, Routes: map[string]config.Route{}})
	data, err := ExportAccounts(cfg, AccountExportRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Proxies) != 0 || data.Accounts[0].ProxyKey != nil || data.Accounts[0].ProxyURL != "" {
		t.Fatalf("proxy leaked without include_proxies: %+v", data)
	}
}

func TestImportRejectsMissingProxyReference(t *testing.T) {
	key := "missing"
	result, err := newTestGateway(t, nil, nil).ImportAccounts(AccountImportRequest{Data: AccountImportData{Type: "sub2api-data", Version: 1, Accounts: []AccountImportItem{{Name: "Gemini API", Platform: "gemini", Type: "apikey", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", APIKey: "secret", ProxyKey: &key}}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountFailed != 1 || !strings.Contains(result.Errors[0].Message, "was not found") {
		t.Fatalf("result=%+v", result)
	}
}

func TestImportReportsInvalidProxyAndAdapterSuggestion(t *testing.T) {
	result, err := newTestGateway(t, nil, nil).ImportAccounts(AccountImportRequest{Data: AccountImportData{Type: "sub2api-data", Version: 1, Proxies: []AccountImportProxy{{ProxyKey: "bad", Protocol: "ftp", Host: "127.0.0.1", Port: 21}}, Accounts: []AccountImportItem{{Name: "Claude old", Platform: "anthropic", Type: "setup-token", Credentials: map[string]any{"access_token": "secret"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProxyFailed != 1 || result.AccountFailed != 1 || result.Errors[1].AdapterID != "cli-proxy-api" {
		t.Fatalf("result=%+v", result)
	}
}
