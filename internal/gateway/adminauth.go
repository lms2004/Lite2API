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
	hash       [sha256.Size]byte
	csrf       string
	expiresAt  time.Time
	createdAt  time.Time
	generation uint64
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
	mu         sync.Mutex
	sessions   map[[sha256.Size]byte]adminSession
	attempts   map[string]loginAttempt
	ttlNanos   atomicDuration
	generation uint64
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
		sessions:   make(map[[sha256.Size]byte]adminSession),
		attempts:   make(map[string]loginAttempt),
		generation: 1,
	}
	a.ttlNanos.Store(ttl)
	return a
}

func (a *AdminAuthenticator) SetTTL(ttl time.Duration) { a.ttlNanos.Store(ttl) }

// Reconfigure applies session policy atomically from the caller's point of
// view. Authentication-boundary changes revoke every cookie immediately;
// bearer authentication continues to use the current runtime token.
func (a *AdminAuthenticator) Reconfigure(ttl time.Duration, revoke bool) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ttlNanos.Store(ttl)
	if revoke {
		a.generation++
		if a.generation == 0 {
			a.generation = 1
		}
		clear(a.sessions)
	}
	return a.generation
}

func (a *AdminAuthenticator) Login(clientIP, candidate, expected string) (string, string, error) {
	return a.login(clientIP, candidate, expected, 0)
}

// LoginAtGeneration binds credential verification and session issuance to the
// runtime state captured by the request. A request that started before an
// authentication-boundary reload cannot issue a cookie after that reload.
func (a *AdminAuthenticator) LoginAtGeneration(clientIP, candidate, expected string, generation uint64) (string, string, error) {
	return a.login(clientIP, candidate, expected, generation)
}

func (a *AdminAuthenticator) login(clientIP, candidate, expected string, generation uint64) (string, string, error) {
	now := time.Now()
	a.mu.Lock()
	if generation != 0 && generation != a.generation {
		a.mu.Unlock()
		return "", "", errors.New("admin authentication generation changed")
	}
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
	return a.issueSessionLocked(now, a.generation)
}

// IssueSession creates a browser session after the caller has independently
// authenticated the request, for example through the admin CIDR/VPN boundary.
func (a *AdminAuthenticator) IssueSession(clientIP string) (string, string, error) {
	return a.issueSession(clientIP, 0)
}

func (a *AdminAuthenticator) IssueSessionAtGeneration(clientIP string, generation uint64) (string, string, error) {
	return a.issueSession(clientIP, generation)
}

func (a *AdminAuthenticator) issueSession(clientIP string, generation uint64) (string, string, error) {
	now := time.Now()
	a.mu.Lock()
	if generation != 0 && generation != a.generation {
		a.mu.Unlock()
		return "", "", errors.New("admin authentication generation changed")
	}
	delete(a.attempts, clientIP)
	return a.issueSessionLocked(now, a.generation)
}

func (a *AdminAuthenticator) issueSessionLocked(now time.Time, generation uint64) (string, string, error) {
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
		expiresAt: now.Add(a.ttlNanos.Load()), generation: generation,
	}
	a.mu.Unlock()
	return token, csrf, nil
}

func (a *AdminAuthenticator) Authenticate(r *http.Request, expected string) (AdminPrincipal, bool) {
	return a.authenticate(r, expected, 0)
}

func (a *AdminAuthenticator) AuthenticateAtGeneration(r *http.Request, expected string, generation uint64) (AdminPrincipal, bool) {
	return a.authenticate(r, expected, generation)
}

func (a *AdminAuthenticator) authenticate(r *http.Request, expected string, generation uint64) (AdminPrincipal, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if generation != 0 && generation != a.generation {
		return AdminPrincipal{}, false
	}
	if token := adminBearerToken(r); token != "" && expected != "" && config.SecureEqual(token, []string{expected}) {
		return AdminPrincipal{}, true
	}
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || len(cookie.Value) < 32 || len(cookie.Value) > 256 {
		return AdminPrincipal{}, false
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	now := time.Now()
	session, ok := a.sessions[hash]
	if ok && !session.expiresAt.After(now) {
		delete(a.sessions, hash)
		ok = false
	}
	if !ok || session.generation != a.generation || (generation != 0 && session.generation != generation) || subtle.ConstantTimeCompare(hash[:], session.hash[:]) != 1 {
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
