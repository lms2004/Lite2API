package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

var ErrConfigRevisionConflict = errors.New("configuration changed since review")

// The revision covers the effective configuration and frozen source identities,
// so a credential/environment reload invalidates an earlier impact review too.
func runtimeConfigRevision(state *runtimeState) string {
	if state == nil {
		return ""
	}
	sources := make(map[string][sha256.Size]byte, len(state.cfg.Accounts))
	for _, account := range state.cfg.Accounts {
		if runtime := state.scheduler.Get(account.ID); runtime != nil {
			sources[account.ID] = runtime.state.identity
		}
	}
	legacy := make([]string, 0, len(state.legacyKeyHashes))
	for digest := range state.legacyKeyHashes {
		legacy = append(legacy, hex.EncodeToString(digest[:]))
	}
	sort.Strings(legacy)
	encoded, _ := json.Marshal(struct {
		Config          config.Config
		Routes          map[string]string
		AdminGeneration uint64
		Sources         map[string][sha256.Size]byte
		Legacy          []string
	}{state.cfg, state.routeFingerprints, state.adminAuthGeneration, sources, legacy})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func requestConfigRevision(r *http.Request) string {
	return strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
}

func (g *Gateway) accessPolicy(state *runtimeState) map[string]any {
	return map[string]any{
		"client_headers":           []string{"Authorization: Bearer", "X-Api-Key"},
		"client_header_precedence": "Authorization_Bearer_then_X-Api-Key",
		"managed":                  map[string]any{"count": len(g.clientKeys.List()), "model_scope": "per_key", "limits": "per_key", "revocation": "new_requests", "in_flight": "continue"},
		"legacy":                   map[string]any{"count": len(state.legacyKeyHashes), "model_scope": "all", "per_key_limits": false, "revocation": "remove_all_config_and_environment_sources_then_reload"},
		"admin":                    map[string]any{"client_access": false, "headers": []string{"Authorization: Bearer", "X-Admin-Token"}, "cookie_writes_require_csrf": true},
		"configuration":            map[string]any{"review_precondition": "If-Match", "without_precondition": "apply_to_current_state", "in_flight": "continue_with_admitted_configuration"},
	}
}

type affectedClientKey struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Models  []string `json:"models"`
}

type accountRemovalImpact struct {
	Revision              string              `json:"config_revision"`
	ObservedAt            string              `json:"observed_at"`
	AccountID             string              `json:"account_id"`
	AffectedModels        []string            `json:"affected_models"`
	RemovedAliases        []string            `json:"removed_aliases"`
	UnavailableModels     []string            `json:"unavailable_models"`
	WildcardAliases       []string            `json:"wildcard_aliases"`
	ImplicitConflicts     []string            `json:"implicit_conflicts"`
	ClientKeys            []affectedClientKey `json:"client_keys"`
	LegacyCredentialCount int                 `json:"legacy_credential_count"`
	Active                int64               `json:"active_requests"`
	CanApply              bool                `json:"can_apply"`
	UnknownModelScope     bool                `json:"unknown_model_scope"`
	InFlight              string              `json:"in_flight"`
	NextAction            string              `json:"next_action"`
}

func (g *Gateway) accountRemovalImpact(state *runtimeState, id string) (accountRemovalImpact, error) {
	impact := accountRemovalImpact{Revision: runtimeConfigRevision(state), ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), AccountID: id, CanApply: true,
		InFlight: "continue_with_admitted_configuration", NextAction: "变更后，用原客户端验证受影响的模型、接口与必要特性；此检查不产生上游调用。"}
	account := state.scheduler.Get(id)
	if account == nil {
		return impact, errors.New("account not found")
	}
	impact.Active = account.Snapshot().Active
	impact.UnknownModelScope = len(account.Config.Models) == 0 || slices.Contains(account.Config.Models, "*")
	models := make(map[string]struct{})
	for alias, route := range state.cfg.Routes {
		if !routeUsesAccount(route, id) {
			continue
		}
		models[alias] = struct{}{}
		if route.AllAccounts {
			impact.WildcardAliases = append(impact.WildcardAliases, alias)
		}
	}
	for _, model := range account.Config.Models {
		if model != "*" {
			models[model] = struct{}{}
		}
	}
	for _, capability := range account.Config.Capabilities {
		models[capability.Model] = struct{}{}
	}
	next := cloneConfig(state.cfg)
	_, _ = removeAccountFromConfig(&next, id)
	for alias := range state.cfg.Routes {
		if _, exists := next.Routes[alias]; !exists {
			impact.RemovedAliases = append(impact.RemovedAliases, alias)
		}
	}
	nextModels := NewScheduler(next).Models()
	for _, model := range state.models {
		if !slices.Contains(nextModels, model) {
			impact.UnavailableModels = append(impact.UnavailableModels, model)
		}
	}
	impact.ImplicitConflicts = implicitlyReexposedModels(state.cfg, next)
	impact.CanApply = len(impact.ImplicitConflicts) == 0
	for model := range models {
		impact.AffectedModels = append(impact.AffectedModels, model)
	}
	sort.Strings(impact.AffectedModels)
	sort.Strings(impact.RemovedAliases)
	sort.Strings(impact.UnavailableModels)
	sort.Strings(impact.WildcardAliases)
	for _, key := range g.clientKeys.List() {
		matched := make([]string, 0)
		for _, model := range impact.AffectedModels {
			if len(key.Models) == 0 || slices.Contains(key.Models, "*") || slices.Contains(key.Models, model) {
				matched = append(matched, model)
			}
		}
		if impact.UnknownModelScope && len(matched) == 0 {
			matched = append(matched, key.Models...)
		}
		if impact.UnknownModelScope || len(matched) > 0 || len(key.Models) == 0 || slices.Contains(key.Models, "*") {
			impact.ClientKeys = append(impact.ClientKeys, affectedClientKey{key.ID, key.Name, key.Enabled, matched})
		}
	}
	impact.LegacyCredentialCount = len(state.legacyKeyHashes)
	return impact, nil
}

func routeUsesAccount(route config.Route, id string) bool {
	if route.AllAccounts || slices.Contains(route.Accounts, id) {
		return true
	}
	for _, target := range route.Targets {
		if target.Account == id {
			return true
		}
	}
	return false
}

// Removing an explicit alias must not make the same name silently fall through
// to unrelated implicit sources. The maintainer must choose that scope first.
func implicitlyReexposedModels(before, after config.Config) []string {
	var removed, conflicts []string
	for alias := range before.Routes {
		if _, exists := after.Routes[alias]; !exists {
			removed = append(removed, alias)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	scheduler := NewScheduler(after)
	for _, alias := range removed {
		for _, account := range scheduler.accounts {
			if account.Config.Enabled && account.supports(alias) {
				conflicts = append(conflicts, alias)
				break
			}
		}
	}
	sort.Strings(conflicts)
	return conflicts
}
