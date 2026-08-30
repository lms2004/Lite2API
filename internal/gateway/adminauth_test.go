package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingAdminBody struct {
	reader  *strings.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingAdminBody) Read(data []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.reader.Read(data)
}

func (*blockingAdminBody) Close() error { return nil }

func TestAdminNetworkRejectsSpoofedForwardedIP(t *testing.T) {
	allowed, _ := parseNetworks([]string{"127.0.0.0/8", "203.0.113.10/32"})
	trusted, _ := parseNetworks([]string{"127.0.0.0/8"})
	untrusted := httptest.NewRequest(http.MethodGet, "/admin", nil)
	untrusted.RemoteAddr = "198.51.100.8:1234"
	untrusted.Header.Set("X-Real-IP", "127.0.0.1")
	if adminNetworkAllowed(untrusted, allowed, trusted) {
		t.Fatal("trusted an X-Real-IP header from an untrusted peer")
	}
	proxied := httptest.NewRequest(http.MethodGet, "/admin", nil)
	proxied.RemoteAddr = "127.0.0.1:1234"
	proxied.Header.Set("X-Real-IP", "198.51.100.8")
	if adminNetworkAllowed(proxied, allowed, trusted) {
		t.Fatal("allowed a public client through the trusted proxy")
	}
	proxied.Header.Set("X-Real-IP", "203.0.113.10")
	if !adminNetworkAllowed(proxied, allowed, trusted) {
		t.Fatal("rejected configured VPN egress address")
	}
}

func TestAdminCookieSessionRequiresCSRF(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	login := httptest.NewRequest(http.MethodPost, "/admin/api/login", strings.NewReader(`{"token":"admin-secret"}`))
	login.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	g.ServeAdminAPI(w, login)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 1 {
		t.Fatalf("login status=%d body=%s", w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe cookie: %+v", cookie)
	}
	principal, ok := g.adminAuth.Authenticate(requestWithCookie(http.MethodPost, cookie), "admin-secret")
	if !ok || !principal.Session {
		t.Fatal("session authentication failed")
	}
	if csrfAllowed(requestWithCookie(http.MethodPost, cookie), principal) {
		t.Fatal("unsafe session request passed without CSRF")
	}
	withCSRF := requestWithCookie(http.MethodPost, cookie)
	withCSRF.Header.Set("X-CSRF-Token", principal.CSRF)
	if !csrfAllowed(withCSRF, principal) {
		t.Fatal("valid CSRF token rejected")
	}
}

func TestAdminAutoLoginRequiresExplicitConfigAndAllowedNetwork(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	withoutOptIn := httptest.NewRequest(http.MethodPost, "/admin/api/login", strings.NewReader(`{}`))
	withoutOptIn.RemoteAddr = "127.0.0.1:1234"
	withoutW := httptest.NewRecorder()
	g.ServeAdminAPI(withoutW, withoutOptIn)
	if withoutW.Code != http.StatusUnauthorized {
		t.Fatalf("tokenless login without opt-in status=%d", withoutW.Code)
	}

	cfg := g.Config()
	cfg.Server.AdminAutoLogin = true
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}

	allowed := httptest.NewRequest(http.MethodPost, "/admin/api/login", strings.NewReader(`{}`))
	allowed.RemoteAddr = "127.0.0.1:1234"
	allowedW := httptest.NewRecorder()
	g.ServeAdminAPI(allowedW, allowed)
	if allowedW.Code != http.StatusOK || len(allowedW.Result().Cookies()) != 1 {
		t.Fatalf("VPN auto login status=%d body=%s", allowedW.Code, allowedW.Body.String())
	}

	blocked := httptest.NewRequest(http.MethodPost, "/admin/api/login", strings.NewReader(`{}`))
	blocked.RemoteAddr = "127.0.0.1:1234"
	blocked.Header.Set("X-Real-IP", "198.51.100.8")
	blockedW := httptest.NewRecorder()
	g.ServeAdminAPI(blockedW, blocked)
	if blockedW.Code != http.StatusNotFound {
		t.Fatalf("non-VPN auto login status=%d", blockedW.Code)
	}
}

func TestAdminTokenRotationRevokesExistingCookieSessions(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	token, _, err := g.adminAuth.Login("127.0.0.1", "admin-secret", "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/api/state", nil)
	req.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: token})
	if _, ok := g.adminAuth.Authenticate(req, "admin-secret"); !ok {
		t.Fatal("session was not valid before token rotation")
	}
	cfg := g.Config()
	cfg.Server.AdminToken = "rotated-admin-secret"
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.adminAuth.Authenticate(req, "rotated-admin-secret"); ok {
		t.Fatal("old cookie remained valid after admin token rotation")
	}
	if _, _, err := g.adminAuth.Login("127.0.0.1", "rotated-admin-secret", "rotated-admin-secret"); err != nil {
		t.Fatalf("new admin token could not create a session: %v", err)
	}
}

func TestAdminLoginStartedBeforeTokenRotationCannotIssueNewSession(t *testing.T) {
	g := newTestGateway(t, nil, nil)
	body := &blockingAdminBody{
		reader:  strings.NewReader(`{"token":"admin-secret"}`),
		started: make(chan struct{}), release: make(chan struct{}),
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/api/login", nil)
	request.Body = body
	request.ContentLength = -1
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.ServeAdminAPI(response, request)
	}()
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("login did not block while decoding its old-generation body")
	}

	cfg := g.Config()
	cfg.Server.AdminToken = "rotated-admin-secret"
	if err := g.store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.Reload(); err != nil {
		t.Fatal(err)
	}
	close(body.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stale login did not finish")
	}
	if response.Code != http.StatusUnauthorized || len(response.Result().Cookies()) != 0 {
		t.Fatalf("stale login issued a current-generation session: status=%d cookies=%v body=%s", response.Code, response.Result().Cookies(), response.Body.String())
	}
}

func requestWithCookie(method string, cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(method, "/admin/api/routes", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.AddCookie(cookie)
	return r
}
