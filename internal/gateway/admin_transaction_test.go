package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lms2004/lite2api/internal/config"
)

func TestConcurrentControlPlaneUpdatesDoNotLoseAccounts(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	const count = 24
	start := make(chan struct{})
	errors := make(chan error, count)
	var group sync.WaitGroup
	for index := 0; index < count; index++ {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			errors <- g.UpsertAccount(config.Account{
				ID: fmt.Sprintf("account-%02d", index), Type: "openai",
				BaseURL: fmt.Sprintf("https://api-%02d.example.com/v1", index),
				APIKey:  "secret", Models: []string{"m"}, Enabled: true,
			})
		}()
	}
	close(start)
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	accounts := g.Config().Accounts
	if len(accounts) != count {
		t.Fatalf("accounts=%d, want %d; concurrent updates were lost", len(accounts), count)
	}
	seen := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		seen[account.ID] = struct{}{}
	}
	for index := 0; index < count; index++ {
		if _, ok := seen[fmt.Sprintf("account-%02d", index)]; !ok {
			t.Fatalf("account-%02d was lost: %+v", index, accounts)
		}
	}
}

func TestCapabilityDiscoveryRebasesOntoLatestAccount(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_, _ = w.Write([]byte(`{"data":[{"id":"new-model"}]}`))
	}))
	defer server.Close()

	g := newTestGateway(t, []config.Account{{
		ID: "discovered", Name: "before", Type: "openai", BaseURL: server.URL + "/v1",
		AuthHeader: "none", Models: []string{"old-model"}, Enabled: true,
	}}, nil)
	done := make(chan error, 1)
	go func() { done <- g.syncDiscoveredCapabilities(context.Background()) }()
	<-entered

	account := g.Config().Accounts[0]
	account.Name = "manually-edited"
	if err := g.UpsertAccount(account); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := g.Config().Accounts[0]
	if got.Name != "manually-edited" {
		t.Fatalf("discovery overwrote concurrent manual edit: %+v", got)
	}
	if !containsModel(got.Models, "new-model") {
		t.Fatalf("discovery was not merged into latest account: %+v", got)
	}
}

func TestDecodeAdminJSONRejectsTrailingValue(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin/api/test", strings.NewReader(`{"ok":true} {"second":true}`))
	var target struct {
		OK bool `json:"ok"`
	}
	if err := decodeAdminJSON(recorder, request, &target); err == nil {
		t.Fatal("expected a second JSON value to be rejected")
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestControlPlaneRejectsUnreloadedExternalEdit(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	external := g.Config()
	external.Accounts = append(external.Accounts, config.Account{
		ID: "external", Type: "openai", BaseURL: "https://external.example.com/v1",
		APIKey: "secret", Models: []string{"m"}, Enabled: true,
	})
	if err := g.store.Save(external); err != nil {
		t.Fatal(err)
	}
	if err := g.UpsertAccount(config.Account{
		ID: "admin", Type: "openai", BaseURL: "https://admin.example.com/v1",
		APIKey: "secret", Models: []string{"m"}, Enabled: true,
	}); err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("update error=%v, want explicit disk conflict", err)
	}
	persisted, err := config.Load(g.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Accounts) != 1 || persisted.Accounts[0].ID != "external" {
		t.Fatalf("external edit was overwritten: %+v", persisted.Accounts)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := g.UpsertAccount(config.Account{
		ID: "admin", Type: "openai", BaseURL: "https://admin.example.com/v1",
		APIKey: "secret", Models: []string{"m"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(g.Config().Accounts) != 2 {
		t.Fatalf("post-reload update did not merge with external config: %+v", g.Config().Accounts)
	}
}

func TestControlPlaneRejectsUnreloadedEnvironmentChange(t *testing.T) {
	t.Setenv("LITE2API_MAX_BODY_BYTES", "2097152")
	g := newTestGateway(t, nil, nil)
	if got := g.Config().Server.MaxBodyBytes; got != 2<<20 {
		t.Fatalf("initial environment overlay=%d", got)
	}
	t.Setenv("LITE2API_MAX_BODY_BYTES", "3145728")
	account := config.Account{
		ID: "admin", Type: "openai", BaseURL: "https://admin.example.com/v1",
		APIKey: "secret", Models: []string{"m"}, Enabled: true,
	}
	if err := g.UpsertAccount(account); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("update error=%v, want environment generation conflict", err)
	}
	if len(g.Config().Accounts) != 0 || g.Config().Server.MaxBodyBytes != 2<<20 {
		t.Fatalf("unreloaded environment change partially mutated state: %+v", g.Config())
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := g.UpsertAccount(account); err != nil {
		t.Fatal(err)
	}
	if got := g.Config().Server.MaxBodyBytes; got != 3<<20 {
		t.Fatalf("reloaded environment overlay=%d", got)
	}
	t.Setenv("LITE2API_MAX_BODY_BYTES", "")
	persisted, err := config.Load(g.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Server.MaxBodyBytes == 3<<20 {
		t.Fatal("environment overlay was persisted by the control-plane update")
	}
}

func TestControlPlaneRejectsUnreloadedSecretEnvironmentChange(t *testing.T) {
	t.Setenv("CONTROLPLANE_UPSTREAM_KEY", "before")
	g := newTestGateway(t, []config.Account{{
		ID: "upstream", Type: "openai", BaseURL: "https://upstream.example.com/v1",
		APIKeyEnv: "CONTROLPLANE_UPSTREAM_KEY", Models: []string{"m"}, Enabled: true,
	}}, nil)
	t.Setenv("CONTROLPLANE_UPSTREAM_KEY", "after")
	account := g.Config().Accounts[0]
	account.Name = "manual edit"
	if err := g.UpsertAccount(account); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("update error=%v, want secret generation conflict", err)
	}
	if g.Config().Accounts[0].Name == "manual edit" {
		t.Fatal("secret rotation caused an implicit partial reload")
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := g.UpsertAccount(account); err != nil {
		t.Fatal(err)
	}
	if got := g.state.Load().scheduler.Get("upstream").UpstreamKey; got != "after" {
		t.Fatalf("runtime upstream key=%q after explicit reload", got)
	}
}

func TestControlPlaneCommitUsesRuntimeLifecycleHooks(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	logActor := g.requestLog.Load()
	session, _, err := g.adminAuth.Login("127.0.0.1", "admin-secret", "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/api/state", nil)
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: session})
	if _, ok := g.adminAuth.Authenticate(request, "admin-secret"); !ok {
		t.Fatal("session was not valid before control-plane commit")
	}

	err = g.updateConfig(func(cfg *config.Config) (bool, error) {
		cfg.Server.AdminToken = "control-plane-rotated"
		cfg.Server.RequestLogPath = "control-plane-requests.jsonl"
		cfg.Server.RequestLogMaxBytes += 64 << 10
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if after := g.requestLog.Load(); after != logActor {
		t.Fatalf("control-plane commit replaced the long-lived log actor: before=%p after=%p", logActor, after)
	}
	status := logActor.Status()
	logPath, _, _ := logActor.configuration()
	if !strings.HasSuffix(logPath, "control-plane-requests.jsonl") || status.MaxBytes != g.Config().Server.RequestLogMaxBytes {
		t.Fatalf("request log lifecycle was bypassed: %+v", status)
	}
	if _, ok := g.adminAuth.Authenticate(request, "control-plane-rotated"); ok {
		t.Fatal("control-plane token rotation did not revoke an existing cookie")
	}
	persisted, err := config.Load(g.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Server.AdminToken != "control-plane-rotated" || persisted.Server.RequestLogPath != "control-plane-requests.jsonl" {
		t.Fatalf("runtime lifecycle commit was not persisted: %+v", persisted.Server)
	}
}

func TestControlPlaneCommitRejectsRestartRequiredFieldsBeforePersist(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	before := g.Config().Server.Listen
	err := g.updateConfig(func(cfg *config.Config) (bool, error) {
		cfg.Server.Listen = "127.0.0.1:65529"
		return true, nil
	})
	if err == nil || !strings.Contains(err.Error(), "process restart") {
		t.Fatalf("restart-required control-plane commit error=%v", err)
	}
	if got := g.Config().Server.Listen; got != before {
		t.Fatalf("runtime listen changed to %q", got)
	}
	persisted, loadErr := config.Load(g.configPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if persisted.Server.Listen != before {
		t.Fatalf("restart-required field persisted as %q", persisted.Server.Listen)
	}
}
