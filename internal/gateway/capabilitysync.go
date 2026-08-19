package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

const capabilityDiscoveryInterval = 10 * time.Minute

type discoveredCapabilityUpdate struct {
	AccountID string
	Catalog   []config.DiscoveredModel
}

type discoveredCapabilityMigration struct {
	AccountID string
	From      string
	To        string
	Legacy    config.ChannelCapability
}

func (g *Gateway) runCapabilityDiscovery(ctx context.Context) {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := g.syncDiscoveredCapabilities(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("upstream capability discovery finished with errors", "error", err)
			}
			timer.Reset(capabilityDiscoveryInterval)
		}
	}
}

func (g *Gateway) syncDiscoveredCapabilities(ctx context.Context) error {
	state := g.state.Load()
	if state == nil {
		return nil
	}
	updates := make([]discoveredCapabilityUpdate, 0, len(state.cfg.Accounts))
	failures := make([]string, 0)
	for _, account := range state.cfg.Accounts {
		if !account.Enabled || !strings.EqualFold(strings.TrimSpace(account.Type), "openai") || strings.TrimSpace(account.BaseURL) == "" {
			continue
		}
		client := state.clients[account.ID]
		if client == nil {
			continue
		}
		catalog, err := discoverModelsForAccount(ctx, client, account)
		if err != nil {
			slog.Debug("upstream model discovery skipped", "account", account.ID, "error", err)
			failures = append(failures, account.ID)
			continue
		}
		catalog = config.FilterDiscoveredCatalog(account, catalog)
		if len(catalog) == 0 {
			continue
		}
		updates = append(updates, discoveredCapabilityUpdate{AccountID: account.ID, Catalog: catalog})
	}

	cfg := cloneConfig(state.cfg)
	changedAccounts := 0
	migrations := make([]discoveredCapabilityMigration, 0)
	for _, update := range updates {
		index := accountIndex(cfg.Accounts, update.AccountID)
		if index < 0 {
			continue
		}
		current := cfg.Accounts[index]
		updated := current
		discoveredIDs := discoveredModelIDs(update.Catalog)
		inferred := config.InferDiscoveredModelCapabilities(updated, update.Catalog)
		if strings.EqualFold(strings.TrimSpace(updated.AdapterID), "cli-proxy-api") {
			var accountMigrations []discoveredCapabilityMigration
			updated.Capabilities, accountMigrations = reconcileDiscoveredCapabilities(current.Capabilities, inferred)
			for migrationIndex := range accountMigrations {
				accountMigrations[migrationIndex].AccountID = update.AccountID
			}
			migrations = append(migrations, accountMigrations...)
			updated.Models = discoveredIDs
		} else {
			updated.Models = appendUniqueModels(current.Models, discoveredIDs)
			updated.Capabilities = mergeSyncedCapabilities(current.Capabilities, inferred)
		}
		// A retained capability may refer to a model that temporarily vanished
		// from a shared /models response. Keep that upstream ID advertised so a
		// valid existing route cannot be made invalid by an incomplete catalog.
		updated.Models = appendUniqueModels(updated.Models, capabilityUpstreamModels(updated.Capabilities))
		if sameModels(current.Models, updated.Models) && sameChannelCapabilities(current.Capabilities, updated.Capabilities) {
			continue
		}
		cfg.Accounts[index] = updated
		changedAccounts++
	}
	if changedAccounts > 0 {
		applyDiscoveredCapabilityMigrations(&cfg, migrations)
		if err := g.saveAndReload(cfg); err != nil {
			return fmt.Errorf("update discovered capabilities: %w", err)
		}
		slog.Info("upstream capabilities synchronized", "accounts", changedAccounts)
	}
	if len(updates) == 0 && len(failures) > 0 {
		return fmt.Errorf("no model catalogs could be refreshed (%d endpoints unavailable)", len(failures))
	}
	return nil
}

func reconcileDiscoveredCapabilities(existing, inferred []config.ChannelCapability) ([]config.ChannelCapability, []discoveredCapabilityMigration) {
	result := append([]config.ChannelCapability(nil), existing...)
	matched := make([]bool, len(result))
	byModel := make(map[string][]int, len(result))
	byUpstream := make(map[string][]int, len(result))
	for index, capability := range result {
		if capability.Model != "" {
			byModel[capability.Model] = append(byModel[capability.Model], index)
		}
		if capability.UpstreamModel != "" {
			byUpstream[capability.UpstreamModel] = append(byUpstream[capability.UpstreamModel], index)
		}
	}
	migrations := make([]discoveredCapabilityMigration, 0)
	for _, capability := range inferred {
		index := firstUnmatchedCapability(byModel[capability.Model], matched)
		if index < 0 {
			index = firstUnmatchedCapability(byUpstream[capability.UpstreamModel], matched)
		}
		if index < 0 {
			result = append(result, capability)
			matched = append(matched, true)
			continue
		}
		previous := result[index]
		capability.ReasoningEfforts = mergeOrderedStrings(previous.ReasoningEfforts, capability.ReasoningEfforts)
		result[index] = capability
		matched[index] = true
		if previous.Model != capability.Model && previous.UpstreamModel == capability.UpstreamModel {
			migrations = append(migrations, discoveredCapabilityMigration{From: previous.Model, To: capability.Model, Legacy: previous})
		}
	}
	return result, migrations
}

func firstUnmatchedCapability(indices []int, matched []bool) int {
	for _, index := range indices {
		if index >= 0 && index < len(matched) && !matched[index] {
			return index
		}
	}
	return -1
}

func capabilityUpstreamModels(capabilities []config.ChannelCapability) []string {
	models := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		if model := strings.TrimSpace(capability.UpstreamModel); model != "" {
			models = append(models, model)
		}
	}
	return models
}

func applyDiscoveredCapabilityMigrations(cfg *config.Config, migrations []discoveredCapabilityMigration) {
	for _, migration := range migrations {
		if migration.From == "" || migration.To == "" || migration.From == migration.To {
			continue
		}
		migrated := false
		for alias, route := range cfg.Routes {
			base, fast := config.ParseRouteModelProfile(route.Model)
			if base != migration.From || !routeTargetsAccount(route, migration.AccountID) {
				continue
			}
			nextModel := migration.To
			if fast {
				nextModel = config.FastProfileModel(nextModel)
			}
			if !routeSupportsModel(*cfg, route, nextModel) {
				continue
			}
			route.Model = nextModel
			cfg.Routes[alias] = route
			migrated = true
		}
		if migrated {
			continue
		}
		// If another fallback target still depends on the old logical name,
		// retain it as a compatibility alias instead of breaking that route.
		index := accountIndex(cfg.Accounts, migration.AccountID)
		if index < 0 || !routeReferencesModel(cfg.Routes, migration.From, migration.AccountID) {
			continue
		}
		if !hasCapability(cfg.Accounts[index].Capabilities, migration.Legacy.Model) {
			cfg.Accounts[index].Capabilities = append(cfg.Accounts[index].Capabilities, migration.Legacy)
		}
		cfg.Accounts[index].Models = appendUniqueModels(cfg.Accounts[index].Models, []string{migration.Legacy.UpstreamModel})
	}
}

func routeTargetsAccount(route config.Route, accountID string) bool {
	for _, target := range route.Targets {
		if target.Account == accountID {
			return true
		}
	}
	return false
}

func routeReferencesModel(routes map[string]config.Route, model, accountID string) bool {
	for _, route := range routes {
		base, _ := config.ParseRouteModelProfile(route.Model)
		if base == model && routeTargetsAccount(route, accountID) {
			return true
		}
	}
	return false
}

func routeSupportsModel(cfg config.Config, route config.Route, model string) bool {
	candidate := route
	candidate.Model = model
	for _, target := range candidate.Targets {
		account, ok := findConfiguredAccount(cfg.Accounts, target.Account)
		if !ok {
			return false
		}
		if _, _, ok := config.ResolveRouteTarget(account, candidate, target); !ok {
			return false
		}
	}
	return true
}

func hasCapability(capabilities []config.ChannelCapability, model string) bool {
	for _, capability := range capabilities {
		if capability.Model == model {
			return true
		}
	}
	return false
}

func discoverModelsForAccount(ctx context.Context, client *http.Client, account config.Account) ([]config.DiscoveredModel, error) {
	if config.UseRichCodexCatalog(account) {
		return fetchDiscoveredCatalog(ctx, client, account, true)
	}
	catalog, err := fetchDiscoveredCatalog(ctx, client, account, false)
	if err != nil {
		return nil, err
	}
	if config.UseRichCodexSupplement(account) {
		// Lite2API's default cliproxy-oauth connection aggregates Claude, Gemini,
		// Antigravity and Codex. Keep its complete /models list, then enrich only
		// the Codex entries with the richer client catalog.
		if rich, richErr := fetchDiscoveredCatalog(ctx, client, account, true); richErr == nil {
			catalog = mergeDiscoveredCatalog(catalog, rich)
		} else {
			slog.Debug("Codex rich catalog supplement unavailable", "account", account.ID, "error", richErr)
		}
	}
	return catalog, nil
}

func fetchDiscoveredCatalog(ctx context.Context, client *http.Client, account config.Account, rich bool) ([]config.DiscoveredModel, error) {
	base, err := url.Parse(strings.TrimSpace(account.BaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("invalid account base URL")
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/models"
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	if rich {
		query := endpoint.Query()
		query.Set("client_version", "lite2api")
		endpoint.RawQuery = query.Encode()
	}

	discoveryCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(discoveryCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if authHeader := strings.TrimSpace(account.AuthHeader); !strings.EqualFold(authHeader, "none") {
		key := account.ResolvedAPIKey()
		if key != "" {
			value := key
			if scheme := strings.TrimSpace(account.AuthScheme); scheme != "" {
				value = scheme + " " + key
			}
			req.Header.Set(authHeader, value)
		}
	}
	for name, value := range account.ResolvedHeaders() {
		req.Header.Set(name, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models endpoint returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	models := decodeDiscoveredModels(data)
	if len(models) == 0 {
		return nil, errors.New("models endpoint returned no model IDs")
	}
	return models, nil
}

func mergeDiscoveredCatalog(base, rich []config.DiscoveredModel) []config.DiscoveredModel {
	result := make([]config.DiscoveredModel, len(base))
	copy(result, base)
	byID := make(map[string]int, len(base)+len(rich))
	for index, model := range result {
		byID[model.ID] = index
	}
	for _, model := range rich {
		if index, exists := byID[model.ID]; exists {
			current := result[index]
			current.ReasoningEfforts = mergeOrderedStrings(current.ReasoningEfforts, model.ReasoningEfforts)
			current.ServiceTiers = mergeOrderedStrings(current.ServiceTiers, model.ServiceTiers)
			result[index] = current
			continue
		}
		byID[model.ID] = len(result)
		result = append(result, model)
	}
	return result
}

func decodeDiscoveredModels(data []byte) []config.DiscoveredModel {
	var envelope struct {
		Data   []json.RawMessage `json:"data"`
		Models []json.RawMessage `json:"models"`
	}
	items := make([]json.RawMessage, 0)
	if err := json.Unmarshal(data, &envelope); err == nil {
		items = append(items, envelope.Data...)
		items = append(items, envelope.Models...)
	}
	if len(items) == 0 {
		var direct []json.RawMessage
		if err := json.Unmarshal(data, &direct); err == nil {
			items = direct
		}
	}

	result := make([]config.DiscoveredModel, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		model := decodeDiscoveredModel(raw)
		if model.ID == "" || model.ID == "*" {
			continue
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		seen[model.ID] = struct{}{}
		result = append(result, model)
	}
	return result
}

func decodeDiscoveredModel(raw json.RawMessage) config.DiscoveredModel {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return config.DiscoveredModel{ID: strings.TrimSpace(text)}
	}
	var item struct {
		ID                       string            `json:"id"`
		Slug                     string            `json:"slug"`
		SupportedReasoningLevels []json.RawMessage `json:"supported_reasoning_levels"`
		ServiceTiers             []json.RawMessage `json:"service_tiers"`
		AdditionalSpeedTiers     []string          `json:"additional_speed_tiers"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return config.DiscoveredModel{}
	}
	id := strings.TrimSpace(item.ID)
	if id == "" {
		id = strings.TrimSpace(item.Slug)
	}
	reasoning := rawStringOrObjectValues(item.SupportedReasoningLevels, "effort")
	tiers := rawStringOrObjectValues(item.ServiceTiers, "id")
	tiers = append(tiers, item.AdditionalSpeedTiers...)
	return config.DiscoveredModel{ID: id, ReasoningEfforts: reasoning, ServiceTiers: tiers}
}

func rawStringOrObjectValues(items []json.RawMessage, key string) []string {
	result := make([]string, 0, len(items))
	for _, raw := range items {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			if text = strings.TrimSpace(text); text != "" {
				result = append(result, text)
			}
			continue
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			continue
		}
		if json.Unmarshal(object[key], &text) == nil {
			if text = strings.TrimSpace(text); text != "" {
				result = append(result, text)
			}
		}
	}
	return result
}

func decodeDiscoveredModelIDs(data []byte) []string {
	return discoveredModelIDs(decodeDiscoveredModels(data))
}

func discoveredModelIDs(models []config.DiscoveredModel) []string {
	result := make([]string, 0, len(models))
	for _, model := range models {
		if id := strings.TrimSpace(model.ID); id != "" {
			result = append(result, id)
		}
	}
	return result
}

func mergeSyncedCapabilities(existing, inferred []config.ChannelCapability) []config.ChannelCapability {
	result := make([]config.ChannelCapability, len(existing))
	copy(result, existing)
	byModel := make(map[string]int, len(existing)+len(inferred))
	for index, capability := range result {
		capability.ReasoningEfforts = append([]string(nil), capability.ReasoningEfforts...)
		result[index] = capability
		if capability.Model != "" {
			byModel[capability.Model] = index
		}
	}
	for _, capability := range inferred {
		if capability.Model == "" || capability.UpstreamModel == "" {
			continue
		}
		if index, exists := byModel[capability.Model]; exists {
			current := result[index]
			current.ReasoningEfforts = mergeOrderedStrings(current.ReasoningEfforts, capability.ReasoningEfforts)
			if current.UpstreamModel == "" {
				current.UpstreamModel = capability.UpstreamModel
			}
			result[index] = current
			continue
		}
		capability.ReasoningEfforts = mergeOrderedStrings(nil, capability.ReasoningEfforts)
		byModel[capability.Model] = len(result)
		result = append(result, capability)
	}
	return result
}

func mergeOrderedStrings(left, right []string) []string {
	result := append([]string(nil), left...)
	seen := make(map[string]struct{}, len(left)+len(right))
	for _, value := range result {
		seen[value] = struct{}{}
	}
	for _, value := range right {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func findConfiguredAccount(accounts []config.Account, id string) (config.Account, bool) {
	for _, account := range accounts {
		if account.ID == id {
			return account, true
		}
	}
	return config.Account{}, false
}
