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
	Models    []string
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
		models, err := discoverModelsForAccount(ctx, client, account)
		if err != nil {
			// Many OpenAI-compatible services do not expose /models. Discovery is
			// opportunistic and must not turn that into an operational incident.
			slog.Debug("upstream model discovery skipped", "account", account.ID, "error", err)
			failures = append(failures, account.ID)
			continue
		}
		models = config.FilterDiscoveredModels(account, models)
		if len(models) == 0 {
			continue
		}
		updates = append(updates, discoveredCapabilityUpdate{AccountID: account.ID, Models: models})
	}

	changedAccounts := 0
	for _, update := range updates {
		current, ok := findConfiguredAccount(g.Config().Accounts, update.AccountID)
		if !ok {
			continue
		}
		updated := current
		updated.Models = appendUniqueModels(current.Models, update.Models)
		inferred := config.InferDiscoveredCapabilities(updated, updated.Models)
		updated.Capabilities = mergeSyncedCapabilities(current.Capabilities, inferred)
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

func discoverModelsForAccount(ctx context.Context, client *http.Client, account config.Account) ([]string, error) {
	base, err := url.Parse(strings.TrimSpace(account.BaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("invalid account base URL")
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/models"
	endpoint.RawQuery = ""
	endpoint.Fragment = ""

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
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	models := decodeDiscoveredModelIDs(data)
	if len(models) == 0 {
		return nil, errors.New("models endpoint returned no model IDs")
	}
	return models, nil
}

func decodeDiscoveredModelIDs(data []byte) []string {
	type modelObject struct {
		ID string `json:"id"`
	}
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
	result := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			var item modelObject
			if json.Unmarshal(raw, &item) != nil {
				continue
			}
			text = item.ID
		}
		text = strings.TrimSpace(text)
		if text == "" || text == "*" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		result = append(result, text)
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
