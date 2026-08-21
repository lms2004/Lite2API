package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

func (g *Gateway) ServeAdminAPI(w http.ResponseWriter, r *http.Request) {
	state := g.state.Load()
	if !adminNetworkAllowed(r, state.adminAllowed, state.trustedProxies) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	path := strings.TrimPrefix(r.URL.Path, "/admin/api")
	if path == "/login" && r.Method == http.MethodPost {
		g.serveAdminLogin(w, r, state)
		return
	}
	principal, authenticated := g.adminAuth.Authenticate(r, state.adminToken)
	if !authenticated {
		writeAPIErrorCode(w, http.StatusUnauthorized, "authentication required", "authentication_error", "invalid_admin_credentials")
		return
	}
	if !csrfAllowed(r, principal) {
		writeAPIErrorCode(w, http.StatusForbidden, "invalid CSRF token", "permission_error", "invalid_csrf_token")
		return
	}
	switch {
	case path == "/session" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "csrf": principal.CSRF, "session": principal.Session})
	case path == "/logout" && r.Method == http.MethodPost:
		g.adminAuth.Logout(r)
		setAdminSessionCookie(w, r, state.trustedProxies, "", -1)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case path == "/state" && r.Method == http.MethodGet:
		stats, accounts := g.Stats(), state.scheduler.Snapshot()
		writeJSON(w, http.StatusOK, map[string]any{"stats": stats, "operations": buildOperationsSnapshot(time.Now(), state.cfg, accounts, stats), "request_log": g.RequestLog(), "accounts": accounts, "models": state.scheduler.Models(), "config": redactedConfig(state.cfg)})
	case path == "/trends" && r.Method == http.MethodGet:
		duration, err := parseTrendRange(r.URL.Query().Get("range"))
		if err != nil {
			writeAPIErrorCode(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_trend_range")
			return
		}
		writeJSON(w, http.StatusOK, g.stats.Trend(time.Now(), duration))
	case path == "/adapters" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"data": g.AdapterCatalog(r.Context(), state.cfg.Accounts)})
	case path == "/prompt-test" && r.Method == http.MethodPost:
		g.servePromptTest(w, r, state)
	case path == "/client-keys" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"data": g.clientKeys.List()})
	case path == "/client-keys" && r.Method == http.MethodPost:
		var input ClientKeyCreate
		if err := decodeAdminJSON(w, r, &input); err != nil {
			return
		}
		key, secret, err := g.clientKeys.Create(input)
		if err != nil {
			writeAPIErrorCode(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_client_key")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"client_key": key, "secret": secret, "warning": "This secret is shown only once."})
	case strings.HasPrefix(path, "/client-keys/") && r.Method == http.MethodPut:
		id := strings.TrimSpace(strings.TrimPrefix(path, "/client-keys/"))
		var input ClientKeyUpdate
		if id == "" || strings.Contains(id, "/") || decodeAdminJSON(w, r, &input) != nil {
			return
		}
		key, err := g.clientKeys.Update(id, input)
		if err != nil {
			writeAPIErrorCode(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_client_key")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"client_key": key})
	case strings.HasPrefix(path, "/client-keys/") && r.Method == http.MethodDelete:
		id := strings.TrimSpace(strings.TrimPrefix(path, "/client-keys/"))
		if id == "" || strings.Contains(id, "/") {
			writeAPIError(w, http.StatusBadRequest, "client key id is required", "invalid_request_error")
			return
		}
		if err := g.clientKeys.Delete(id); err != nil {
			writeAPIErrorCode(w, http.StatusNotFound, err.Error(), "invalid_request_error", "client_key_not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case path == "/reload" && r.Method == http.MethodPost:
		if err := g.Reload(); err != nil {
			writeAPIError(w, 400, err.Error(), "config_error")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case path == "/oauth/start" && r.Method == http.MethodPost:
		g.serveOAuthStart(w, r)
	case path == "/oauth/callback" && r.Method == http.MethodPost:
		g.serveOAuthCallback(w, r)
	case path == "/oauth/status" && r.Method == http.MethodPost:
		g.serveOAuthStatus(w, r)
	case path == "/oauth/accounts" && r.Method == http.MethodGet:
		g.serveOAuthAccounts(w, r)
	case path == "/oauth/accounts" && r.Method == http.MethodDelete:
		g.serveOAuthAccountDelete(w, r)
	case path == "/oauth/accounts/status" && r.Method == http.MethodPost:
		g.serveOAuthAccountStatus(w, r)
	case path == "/accounts/test" && r.Method == http.MethodPost:
		g.serveAccountTest(w, r, state)
	case path == "/accounts/export" && r.Method == http.MethodPost:
		var input AccountExportRequest
		if err := decodeAdminJSON(w, r, &input); err != nil {
			return
		}
		data, err := ExportAccounts(state.cfg, input)
		if err != nil {
			writeAPIErrorCode(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_account_export")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	case path == "/accounts/import" && r.Method == http.MethodPost:
		var input AccountImportRequest
		if err := decodeAdminJSON(w, r, &input); err != nil {
			return
		}
		result, err := g.ImportAccounts(r.Context(), input)
		if err != nil {
			writeAPIErrorCode(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_account_import")
			return
		}
		writeJSON(w, http.StatusOK, result)
	case path == "/accounts" && r.Method == http.MethodPut:
		var account config.Account
		if err := decodeAdminJSON(w, r, &account); err != nil {
			return
		}
		if err := g.UpsertAccount(account); err != nil {
			writeAPIError(w, 400, err.Error(), "config_error")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case strings.HasPrefix(path, "/accounts/") && r.Method == http.MethodDelete:
		id := strings.TrimSpace(strings.TrimPrefix(path, "/accounts/"))
		if id == "" {
			writeAPIError(w, 400, "account id is required", "config_error")
			return
		}
		if err := g.DeleteAccount(id); err != nil {
			writeAPIError(w, 400, err.Error(), "config_error")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case path == "/routes" && r.Method == http.MethodPut:
		var routes map[string]config.Route
		if err := decodeAdminJSON(w, r, &routes); err != nil {
			return
		}
		if err := g.ReplaceRoutes(routes); err != nil {
			writeAPIError(w, 400, err.Error(), "config_error")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	default:
		writeAPIError(w, 404, "admin endpoint not found", "not_found")
	}
}

func parseTrendRange(value string) (time.Duration, error) {
	switch strings.TrimSpace(value) {
	case "", "24h":
		return 24 * time.Hour, nil
	case "1h":
		return time.Hour, nil
	case "6h":
		return 6 * time.Hour, nil
	case "3d":
		return 3 * 24 * time.Hour, nil
	case "7d":
		return trendRetention, nil
	case "all":
		// The frontend tightens this range to the actual retained points.
		return trendRetention, nil
	default:
		return 0, errors.New("range must be one of 1h, 6h, 24h, 3d, 7d or all")
	}
}

func (g *Gateway) serveAdminLogin(w http.ResponseWriter, r *http.Request, state *runtimeState) {
	var input struct {
		Token string `json:"token"`
	}
	if err := decodeAdminJSON(w, r, &input); err != nil {
		return
	}
	clientIP := effectiveClientIP(r, state.trustedProxies)
	ip := "unknown"
	if clientIP != nil {
		ip = clientIP.String()
	}
	var session, csrf string
	var err error
	if state.cfg.Server.AdminAutoLogin {
		session, csrf, err = g.adminAuth.IssueSession(ip)
	} else {
		session, csrf, err = g.adminAuth.Login(ip, input.Token, state.adminToken)
	}
	if errors.Is(err, ErrAdminLoginLocked) {
		w.Header().Set("Retry-After", strconv.Itoa(15*60))
		writeAPIErrorCode(w, http.StatusTooManyRequests, "too many login attempts", "rate_limit_error", "admin_login_locked")
		return
	}
	if err != nil {
		writeAPIErrorCode(w, http.StatusUnauthorized, "invalid admin credentials", "authentication_error", "invalid_admin_credentials")
		return
	}
	ttl := state.cfg.Server.AdminSessionTTL.Duration
	setAdminSessionCookie(w, r, state.trustedProxies, session, int(ttl.Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "csrf": csrf, "expires_in": int(ttl.Seconds())})
}

func decodeAdminJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeAPIError(w, 400, "invalid JSON: "+err.Error(), "invalid_request_error")
		return err
	}
	return nil
}

func (g *Gateway) UpsertAccount(account config.Account) error {
	cfg := cloneConfig(g.state.Load().cfg)
	found := false
	for i := range cfg.Accounts {
		if cfg.Accounts[i].ID != account.ID {
			continue
		}
		if account.APIKey == "" && account.APIKeyEnv == "" {
			account.APIKey, account.APIKeyEnv = cfg.Accounts[i].APIKey, cfg.Accounts[i].APIKeyEnv
		}
		if len(account.Capabilities) == 0 && len(cfg.Accounts[i].Capabilities) > 0 {
			account.Capabilities = cfg.Accounts[i].Capabilities
		}
		for name, value := range account.Headers {
			if value == "********" {
				account.Headers[name] = cfg.Accounts[i].Headers[name]
			}
		}
		cfg.Accounts[i] = account
		found = true
		break
	}
	if !found {
		cfg.Accounts = append(cfg.Accounts, account)
	}
	return g.saveAndReload(cfg)
}

func (g *Gateway) DeleteAccount(id string) error {
	cfg := cloneConfig(g.state.Load().cfg)
	next := cfg.Accounts[:0]
	found := false
	for _, account := range cfg.Accounts {
		if account.ID == id {
			found = true
			continue
		}
		next = append(next, account)
	}
	if !found {
		return errors.New("account not found")
	}
	cfg.Accounts = next
	for model, route := range cfg.Routes {
		filtered := route.Accounts[:0]
		for _, accountID := range route.Accounts {
			if accountID != id {
				filtered = append(filtered, accountID)
			}
		}
		route.Accounts = filtered
		filteredTargets := route.Targets[:0]
		for _, target := range route.Targets {
			if target.Account != id {
				filteredTargets = append(filteredTargets, target)
			}
		}
		route.Targets = filteredTargets
		cfg.Routes[model] = route
	}
	return g.saveAndReload(cfg)
}

func (g *Gateway) ReplaceRoutes(routes map[string]config.Route) error {
	cfg := cloneConfig(g.state.Load().cfg)
	cfg.Routes = routes
	return g.saveAndReload(cfg)
}

func (g *Gateway) saveAndReload(cfg config.Config) error {
	g.reloadMu.Lock()
	defer g.reloadMu.Unlock()
	state, err := g.buildState(cfg)
	if err != nil {
		return err
	}
	if err := g.store.Save(cfg); err != nil {
		return err
	}
	g.swapState(state)
	return nil
}

func cloneConfig(cfg config.Config) config.Config {
	data, _ := json.Marshal(cfg)
	var result config.Config
	_ = json.Unmarshal(data, &result)
	return result
}

func redactedConfig(cfg config.Config) config.Config {
	result := cloneConfig(cfg)
	result.Server.APIKeys = nil
	result.Server.AdminToken = ""
	for i := range result.Accounts {
		if result.Accounts[i].APIKey != "" {
			result.Accounts[i].APIKey = "********"
		}
		for name := range result.Accounts[i].Headers {
			result.Accounts[i].Headers[name] = "********"
		}
	}
	return result
}
