package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

const adminSessionCookie = "lite2api_admin_session"

var ErrAdminLoginLocked = errors.New("too many login attempts")

type adminSession struct {
	hash      [sha256.Size]byte
	csrf      string
	expiresAt time.Time
	createdAt time.Time
}

type loginAttempt struct {
	windowStart time.Time
	failures    int
	lockedUntil time.Time
}

type AdminPrincipal struct {
	Session bool
	CSRF    string
}

type AdminAuthenticator struct {
	mu       sync.Mutex
	sessions map[[sha256.Size]byte]adminSession
	attempts map[string]loginAttempt
	ttlNanos atomicDuration
}

type atomicDuration struct {
	mu sync.RWMutex
	d  time.Duration
}

func (a *atomicDuration) Store(value time.Duration) {
	a.mu.Lock()
	a.d = value
	a.mu.Unlock()
}

func (a *atomicDuration) Load() time.Duration {
	a.mu.RLock()
	value := a.d
	a.mu.RUnlock()
	return value
}

func NewAdminAuthenticator(ttl time.Duration) *AdminAuthenticator {
	a := &AdminAuthenticator{
		sessions: make(map[[sha256.Size]byte]adminSession),
		attempts: make(map[string]loginAttempt),
	}
	a.ttlNanos.Store(ttl)
	return a
}

func (a *AdminAuthenticator) SetTTL(ttl time.Duration) { a.ttlNanos.Store(ttl) }

func (a *AdminAuthenticator) Login(clientIP, candidate, expected string) (string, string, error) {
	now := time.Now()
	a.mu.Lock()
	attempt := a.attempts[clientIP]
	if attempt.lockedUntil.After(now) {
		a.mu.Unlock()
		return "", "", ErrAdminLoginLocked
	}
	if attempt.windowStart.IsZero() || now.Sub(attempt.windowStart) > 5*time.Minute {
		attempt = loginAttempt{windowStart: now}
	}
	if expected == "" || !config.SecureEqual(candidate, []string{expected}) {
		attempt.failures++
		if attempt.failures >= 5 {
			attempt.lockedUntil = now.Add(15 * time.Minute)
		}
		a.attempts[clientIP] = attempt
		a.trimAttemptsLocked(now)
		a.mu.Unlock()
		return "", "", errors.New("invalid admin credentials")
	}
	delete(a.attempts, clientIP)
	a.removeExpiredSessionsLocked(now)
	if len(a.sessions) >= 32 {
		var oldestHash [sha256.Size]byte
		var oldest time.Time
		for hash, session := range a.sessions {
			if oldest.IsZero() || session.createdAt.Before(oldest) {
				oldestHash, oldest = hash, session.createdAt
			}
		}
		delete(a.sessions, oldestHash)
	}
	token, err := randomURLToken(32)
	if err != nil {
		a.mu.Unlock()
		return "", "", err
	}
	csrf, err := randomURLToken(24)
	if err != nil {
		a.mu.Unlock()
		return "", "", err
	}
	hash := sha256.Sum256([]byte(token))
	a.sessions[hash] = adminSession{
		hash: hash, csrf: csrf, createdAt: now,
		expiresAt: now.Add(a.ttlNanos.Load()),
	}
	a.mu.Unlock()
	return token, csrf, nil
}

func (a *AdminAuthenticator) Authenticate(r *http.Request, expected string) (AdminPrincipal, bool) {
	if token := adminBearerToken(r); token != "" && expected != "" && config.SecureEqual(token, []string{expected}) {
		return AdminPrincipal{}, true
	}
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || len(cookie.Value) < 32 || len(cookie.Value) > 256 {
		return AdminPrincipal{}, false
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	now := time.Now()
	a.mu.Lock()
	session, ok := a.sessions[hash]
	if ok && !session.expiresAt.After(now) {
		delete(a.sessions, hash)
		ok = false
	}
	a.mu.Unlock()
	if !ok || subtle.ConstantTimeCompare(hash[:], session.hash[:]) != 1 {
		return AdminPrincipal{}, false
	}
	return AdminPrincipal{Session: true, CSRF: session.csrf}, true
}

func (a *AdminAuthenticator) Logout(r *http.Request) {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	a.mu.Lock()
	delete(a.sessions, hash)
	a.mu.Unlock()
}

func (a *AdminAuthenticator) removeExpiredSessionsLocked(now time.Time) {
	for hash, session := range a.sessions {
		if !session.expiresAt.After(now) {
			delete(a.sessions, hash)
		}
	}
}

func (a *AdminAuthenticator) trimAttemptsLocked(now time.Time) {
	if len(a.attempts) <= 1024 {
		return
	}
	for ip, attempt := range a.attempts {
		if now.Sub(attempt.windowStart) > 30*time.Minute && !attempt.lockedUntil.After(now) {
			delete(a.attempts, ip)
		}
	}
}

func adminBearerToken(r *http.Request) string {
	if token := bearerToken(r.Header.Get("Authorization")); token != "" {
		return token
	}
	return strings.TrimSpace(r.Header.Get("X-Admin-Token"))
}

func apiBearerToken(r *http.Request) string {
	if token := bearerToken(r.Header.Get("Authorization")); token != "" {
		return token
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}

func bearerToken(header string) string {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return ""
	}
	return fields[1]
}

func randomURLToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func csrfAllowed(r *http.Request, principal AdminPrincipal) bool {
	if !principal.Session || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	provided := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
	if len(provided) != len(principal.CSRF) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(principal.CSRF)) == 1
}

func parseNetworks(values []string) ([]*net.IPNet, error) {
	result := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, err
		}
		result = append(result, network)
	}
	return result, nil
}

func networkContains(networks []*net.IPNet, ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

func effectiveClientIP(r *http.Request, trustedProxies []*net.IPNet) net.IP {
	peer := remoteIP(r)
	if !networkContains(trustedProxies, peer) {
		return peer
	}
	value := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if strings.ContainsAny(value, ", \t\r\n") {
		return peer
	}
	if forwarded := net.ParseIP(value); forwarded != nil {
		return forwarded
	}
	return peer
}

func adminNetworkAllowed(r *http.Request, allowed, trusted []*net.IPNet) bool {
	return networkContains(allowed, effectiveClientIP(r, trusted))
}

func requestWasHTTPS(r *http.Request, trusted []*net.IPNet) bool {
	if r.TLS != nil {
		return true
	}
	return networkContains(trusted, remoteIP(r)) && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func adminCookiePath(r *http.Request, trusted []*net.IPNet) string {
	if networkContains(trusted, remoteIP(r)) {
		if prefix := strings.TrimSpace(r.Header.Get("X-Forwarded-Prefix")); prefix == "/lite-admin" {
			return prefix
		}
	}
	return "/admin"
}

func setAdminSessionCookie(w http.ResponseWriter, r *http.Request, trusted []*net.IPNet, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: adminSessionCookie, Value: value, Path: adminCookiePath(r, trusted),
		MaxAge: maxAge, HttpOnly: true, Secure: requestWasHTTPS(r, trusted),
		SameSite: http.SameSiteStrictMode,
	})
}
