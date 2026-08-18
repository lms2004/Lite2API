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

// runCapabilityDiscovery keeps adapter-backed model catalogs fresh without
// making the request path depend on discovery. A failed /models endpoint never
// disables an otherwise working account; the last known capabilities remain in
// place until a later successful observation.
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
			// Many OpenAI-compatible services do not expose /models. Discovery is
			// opportunistic and must not turn that into an operational incident.
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

	changedAccounts := 0
	for _, update := range updates {
		current, ok := findConfiguredAccount(g.Config().Accounts, update.AccountID)
		if !ok {
			continue
		}
		updated := current
		discoveredIDs := discoveredModelIDs(update.Catalog)
		inferred := config.InferDiscoveredModelCapabilities(updated, update.Catalog)
		if strings.EqualFold(strings.TrimSpace(updated.AdapterID), "cli-proxy-api") {
			// CLIProxy is an auto-managed live capability source. Replacing its
			// discovered catalog prevents retired models and tiers from lingering.
			updated.Models = discoveredIDs
			updated.Capabilities = inferred
		} else {
			// Generic OpenAI-compatible accounts may include manually declared
			// aliases/capabilities, so discovery enriches rather than erases them.
			updated.Models = appendUniqueModels(current.Models, discoveredIDs)
			updated.Capabilities = mergeSyncedCapabilities(current.Capabilities, inferred)
		}
		if sameModels(current.Models, updated.Models) && sameChannelCapabilities(current.Capabilities, updated.Capabilities) {
			continue
		}
		if err := g.UpsertAccount(updated); err != nil {
			return fmt.Errorf("update discovered capabilities for %s: %w", update.AccountID, err)
		}
		changedAccounts++
	}
	if changedAccounts > 0 {
		slog.Info("upstream capabilities synchronized", "accounts", changedAccounts)
	}
	if len(updates) == 0 && len(failures) > 0 {
		return fmt.Errorf("no model catalogs could be refreshed (%d endpoints unavailable)", len(failures))
	}
	return nil
}

func discoverModelsForAccount(ctx context.Context, client *http.Client, account config.Account) ([]config.DiscoveredModel, error) {
	base, err := url.Parse(strings.TrimSpace(account.BaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("invalid account base URL")
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/models"
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	if config.UseRichCodexCatalog(account) {
		// CLIProxyAPI returns its rich Codex client catalog whenever the query
		// contains client_version. It includes reasoning and service-tier metadata.
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
