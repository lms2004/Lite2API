package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/lms2004/lite2api/internal/config"
)

func (g *Gateway) ServeAdminAPI(w http.ResponseWriter, r *http.Request) {
	state := g.state.Load()
	if !adminAuthenticated(r, state.adminToken) {
		writeAPIError(w, http.StatusUnauthorized, "invalid admin token", "authentication_error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	path := strings.TrimPrefix(r.URL.Path, "/admin/api")
	switch {
	case path == "/state" && r.Method == http.MethodGet:
		writeJSON(w, 200, map[string]any{"stats": g.Stats(), "accounts": g.Accounts(), "models": state.scheduler.Models(), "config": redactedConfig(state.cfg)})
	case path == "/reload" && r.Method == http.MethodPost:
		if err := g.Reload(); err != nil {
			writeAPIError(w, 400, err.Error(), "config_error")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
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

func adminAuthenticated(r *http.Request, expected string) bool {
	if expected == "" {
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-Admin-Token"))
	}
	return config.SecureEqual(token, []string{expected})
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
