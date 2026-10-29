package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestAdminSnapshotsRedactURLCredentialsAndPreserveRoundTrip(t *testing.T) {
	const base = "https://api.example.com/v1?access_token=query-canary&api-version=2026-01-01"
	const proxy = "http://proxy-user:proxy-password-canary@127.0.0.1:8080"
	g := newTestGateway(t, []config.Account{{
		ID: "a", Name: "Before", Type: "openai", BaseURL: base, ProxyURL: proxy,
		APIKey: "api-key-canary", Headers: map[string]string{"X-Private": "header-canary"},
		Models: []string{"m"}, Enabled: true, Weight: 1,
	}}, nil)
	request := httptest.NewRequest(http.MethodGet, "/admin/api/state", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, request)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	for _, secret := range []string{"query-canary", "proxy-user", "proxy-password-canary", "api-key-canary", "header-canary"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("management snapshot leaked %q", secret)
		}
	}
	account := redactedConfig(g.Config()).Accounts[0]
	account.Name = "After"
	if err := g.UpsertAccount(account); err != nil {
		t.Fatal(err)
	}
	updated := g.Config().Accounts[0]
	if updated.Name != "After" || updated.BaseURL != base || updated.ProxyURL != proxy || updated.APIKey != "api-key-canary" || updated.Headers["X-Private"] != "header-canary" {
		t.Fatal("updating a displayed account lost existing secret material")
	}
	account = redactedConfig(g.Config()).Accounts[0]
	account.BaseURL = strings.Replace(account.BaseURL, "api.example.com", "other.example.com", 1)
	if err := g.UpsertAccount(account); err == nil || !strings.Contains(err.Error(), "complete value") {
		t.Fatalf("partially edited masked URL accepted: %v", err)
	}
	if g.Config().Accounts[0].BaseURL != base {
		t.Fatal("rejected update changed active configuration")
	}
	exported, err := ExportAccounts(g.Config(), AccountExportRequest{IncludeProxies: true})
	if err != nil {
		t.Fatal(err)
	}
	if exported.Accounts[0].BaseURL != base || exported.Accounts[0].APIKey != "api-key-canary" || exported.Proxies[0].Password != "proxy-password-canary" {
		t.Fatal("explicit credential export is no longer restorable")
	}
}

func TestAccountImportHandlesDisplayedSecretsWithoutSavingPlaceholders(t *testing.T) {
	const base = "https://api.example.com/v1?key=query-canary"
	g := newTestGateway(t, []config.Account{{
		ID: "a", Name: "Before", Type: "openai", BaseURL: base,
		APIKey: "api-key-canary", Headers: map[string]string{"X-Private": "header-canary"},
		Models: []string{"m"}, Enabled: true, Weight: 1,
	}}, nil)
	displayed := redactedConfig(g.Config()).Accounts[0]
	item := AccountImportItem{
		ID: displayed.ID, Name: "After", Type: displayed.Type, BaseURL: displayed.BaseURL,
		APIKey: displayed.APIKey, Headers: displayed.Headers, Models: displayed.Models,
	}
	request := AccountImportRequest{Mode: "upsert", Data: newAccountImportData([]AccountImportItem{item})}
	result, err := g.ImportAccounts(context.Background(), request)
	if err != nil || !result.Applied || result.AccountUpdated != 1 {
		t.Fatalf("displayed-account update result=%+v err=%v", result, err)
	}
	actual := g.Config().Accounts[0]
	if actual.Name != "After" || actual.APIKey != "api-key-canary" || actual.BaseURL != base || actual.Headers["X-Private"] != "header-canary" {
		t.Fatal("import persisted a placeholder or lost the existing credential")
	}
	item.ID = "new"
	request.Data = newAccountImportData([]AccountImportItem{item})
	result, err = g.ImportAccounts(context.Background(), request)
	if err != nil || result.Applied || result.AccountFailed != 1 || len(g.Config().Accounts) != 1 {
		t.Fatalf("new masked account was not rejected: result=%+v err=%v", result, err)
	}
}

func TestAccountConnectionTestRestoresDisplayedSecrets(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("key") != "query-canary" || r.Header.Get("Authorization") != "Bearer api-key-canary" || r.Header.Get("X-Private") != "header-canary" {
			t.Error("connection test did not use the stored credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer upstream.Close()
	g := newTestGateway(t, []config.Account{{
		ID: "a", Type: "openai", BaseURL: upstream.URL + "/v1?key=query-canary",
		APIKey: "api-key-canary", Headers: map[string]string{"X-Private": "header-canary"},
		Models: []string{"m"}, Enabled: true, Weight: 1,
	}}, nil)
	body, err := json.Marshal(accountTestRequest{Account: redactedConfig(g.Config()).Accounts[0]})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/admin/api/accounts/test", strings.NewReader(string(body)))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer admin-secret")
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, r)
	if w.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
	}
	for _, secret := range []string{"query-canary", "api-key-canary", "header-canary"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("connection test exposed %q", secret)
		}
	}
}
