package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminNetworkRejectsSpoofedForwardedIP(t *testing.T) {
	allowed, _ := parseNetworks([]string{"127.0.0.0/8", "212.17.236.178/32"})
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
	proxied.Header.Set("X-Real-IP", "212.17.236.178")
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

func requestWithCookie(method string, cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(method, "/admin/api/routes", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.AddCookie(cookie)
	return r
}
