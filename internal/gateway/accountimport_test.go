package gateway

import (
	"strings"
	"testing"
)

func TestAccountImportPreviewAndApply(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	enabled := true
	request := AccountImportRequest{
		Data: newAccountImportData([]AccountImportItem{{
			ID: "atom-imported", Name: "Atom imported", Type: "openai",
			BaseURL: "http://127.0.0.1:45678/v1", AuthHeader: "none",
			Models: []string{"deepseek-fast"}, Concurrency: 2, Enabled: &enabled,
		}}),
		DryRun: true,
	}
	preview, err := g.ImportAccounts(request)
	if err != nil {
		t.Fatal(err)
	}
	if preview.AccountCreated != 1 || preview.Applied || len(g.Config().Accounts) != 0 {
		t.Fatalf("preview=%+v accounts=%d", preview, len(g.Config().Accounts))
	}

	request.DryRun = false
	result, err := g.ImportAccounts(request)
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountCreated != 1 || !result.Applied || len(g.Config().Accounts) != 1 {
		t.Fatalf("result=%+v accounts=%d", result, len(g.Config().Accounts))
	}

	skipped, err := g.ImportAccounts(request)
	if err != nil {
		t.Fatal(err)
	}
	if skipped.AccountSkipped != 1 || skipped.Applied {
		t.Fatalf("skip result=%+v", skipped)
	}
}

func TestAccountImportUpsertPreservesCredential(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	enabled := true
	create := AccountImportRequest{Data: newAccountImportData([]AccountImportItem{{
		ID: "api", Name: "Before", Type: "openai", BaseURL: "https://api.example.com/v1",
		APIKey: "secret", Models: []string{"m"}, Enabled: &enabled,
	}})}
	if _, err := g.ImportAccounts(create); err != nil {
		t.Fatal(err)
	}
	update := AccountImportRequest{Mode: "upsert", Data: newAccountImportData([]AccountImportItem{{
		ID: "api", Name: "After", Type: "openai", BaseURL: "https://api.example.com/v1",
		Models: []string{"m2"}, Enabled: &enabled,
	}})}
	result, err := g.ImportAccounts(update)
	if err != nil {
		t.Fatal(err)
	}
	account := g.Config().Accounts[0]
	if result.AccountUpdated != 1 || account.Name != "After" || account.APIKey != "secret" {
		t.Fatalf("result=%+v account=%+v", result, account)
	}
}

func TestAccountImportPartialSuccess(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	enabled := true
	request := AccountImportRequest{Data: newAccountImportData([]AccountImportItem{
		{ID: "good", BaseURL: "http://127.0.0.1:45678/v1", AuthHeader: "none", Enabled: &enabled},
		{ID: "bad", Name: "OAuth without adapter", Platform: "claude", Type: "oauth", Credentials: map[string]any{"refresh_token": "not-an-api-key"}, Enabled: &enabled},
	})}
	result, err := g.ImportAccounts(request)
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountCreated != 1 || result.AccountFailed != 1 || !result.Applied {
		t.Fatalf("result=%+v", result)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Message, "external adapter") {
		t.Fatalf("errors=%+v", result.Errors)
	}
	if len(g.Config().Accounts) != 1 || g.Config().Accounts[0].ID != "good" {
		t.Fatalf("accounts=%+v", g.Config().Accounts)
	}
}

func TestSub2APIDataSubsetMapsAPIKeyAndProxy(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	proxyKey := "proxy-a"
	request := AccountImportRequest{Data: AccountImportData{
		Type: "sub2api-data", Version: 1,
		Proxies: []AccountImportProxy{{
			ProxyKey: proxyKey, Protocol: "socks5", Host: "127.0.0.1", Port: 1080,
			Username: "user", Password: "pass",
		}},
		Accounts: []AccountImportItem{{
			Name: "Imported DeepSeek", Platform: "deepseek", Type: "api_key",
			Credentials: map[string]any{"api_key": "secret"},
			Extra: map[string]any{
				"base_url": "https://api.deepseek.com/v1",
				"models":   []any{"deepseek-chat", "deepseek-reasoner"},
			},
			ProxyKey: &proxyKey, Concurrency: 3,
		}},
	}}
	result, err := g.ImportAccounts(request)
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountCreated != 1 || result.SourceFormat != "sub2api-data" {
		t.Fatalf("result=%+v", result)
	}
	account := g.Config().Accounts[0]
	if account.ID != "deepseek-imported-deepseek" || account.APIKey != "secret" || account.ProxyURL != "socks5://user:pass@127.0.0.1:1080" {
		t.Fatalf("account=%+v", account)
	}
	if len(account.Models) != 2 {
		t.Fatalf("models=%v", account.Models)
	}
}

func TestAccountImportRejectsUnsupportedHeader(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	_, err := g.ImportAccounts(AccountImportRequest{Data: AccountImportData{
		Type: "unknown-export", Version: 1, Accounts: []AccountImportItem{{ID: "a"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "unsupported import type") {
		t.Fatalf("err=%v", err)
	}
}

func TestAccountImportOAuthWithBaseURLStillRequiresAdapterCredential(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	result, err := g.ImportAccounts(AccountImportRequest{Data: newAccountImportData([]AccountImportItem{{
		Name: "OAuth export", Type: "oauth", BaseURL: "https://adapter.example.com/v1",
		Credentials: map[string]any{"refresh_token": "not-an-api-key"},
	}})})
	if err != nil {
		t.Fatal(err)
	}
	if result.AccountFailed != 1 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Message, "external adapter") {
		t.Fatalf("result=%+v", result)
	}
}

func TestAccountImportLimit(t *testing.T) {
	accounts := make([]AccountImportItem, accountImportLimit+1)
	_, err := newTestGateway(t, nil, nil).ImportAccounts(AccountImportRequest{Data: newAccountImportData(accounts)})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err=%v", err)
	}
}
