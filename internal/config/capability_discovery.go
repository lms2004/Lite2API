package config

import (
	"net/url"
	"strings"
)

// DiscoveredModel is the provider-neutral subset of model catalog metadata the
// router needs. Discovery deliberately ignores presentation-only fields.
type DiscoveredModel struct {
	ID               string
	ReasoningEfforts []string
	ServiceTiers     []string
}

// UseRichCodexCatalog reports whether this CLIProxy connection represents the
// Codex credential family. Aggregate CLIProxy pools must keep using the normal
// /models list because the rich Codex endpoint intentionally contains Codex
// models only.
func UseRichCodexCatalog(account Account) bool {
	return discoveryScope(account) == "codex"
}

// FilterDiscoveredModels scopes a shared adapter's /models response to the
// channel represented by account. Generic OpenAI-compatible accounts retain the
// full response. CLIProxy often exposes several credential families behind one
// endpoint, so a provider-specific account must not suddenly claim every model
// served by the shared process.
func FilterDiscoveredModels(account Account, models []string) []string {
	result := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, raw := range models {
		model := strings.TrimSpace(raw)
		if model == "" || model == "*" || !modelMatchesDiscoveryScope(discoveryScope(account), model) {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	return result
}

// FilterDiscoveredCatalog applies the same provider scope while preserving rich
// capability metadata such as reasoning levels and service tiers.
func FilterDiscoveredCatalog(account Account, models []DiscoveredModel) []DiscoveredModel {
	result := make([]DiscoveredModel, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	scope := discoveryScope(account)
	for _, raw := range models {
		model := raw
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" || model.ID == "*" || !modelMatchesDiscoveryScope(scope, model.ID) {
			continue
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		seen[model.ID] = struct{}{}
		model.ReasoningEfforts = unionStrings(nil, normalizeDiscoveredReasoning(model.ReasoningEfforts))
		model.ServiceTiers = unionStrings(nil, normalizeDiscoveredTiers(model.ServiceTiers))
		result = append(result, model)
	}
	return result
}

// InferDiscoveredCapabilities keeps the string-only compatibility path used by
// existing callers and tests. Rich catalog discovery should prefer
// InferDiscoveredModelCapabilities.
func InferDiscoveredCapabilities(account Account, models []string) []ChannelCapability {
	catalog := make([]DiscoveredModel, 0, len(models))
	for _, model := range models {
		catalog = append(catalog, DiscoveredModel{ID: model})
	}
	return InferDiscoveredModelCapabilities(account, catalog)
}

// InferDiscoveredModelCapabilities converts a live provider catalog into
// logical route capabilities. Provider metadata wins over local heuristics. A
// service tier is represented as a logical execution profile (for example
// sol@fast) while keeping the concrete upstream model unchanged.
func InferDiscoveredModelCapabilities(account Account, catalog []DiscoveredModel) []ChannelCapability {
	catalog = FilterDiscoveredCatalog(account, catalog)
	capabilities := make([]ChannelCapability, 0, len(catalog)*2)
	for _, item := range catalog {
		logicalModel, fallbackEfforts := inferDiscoveredLogicalModel(account, item.ID)
		if logicalModel == "" {
			continue
		}
		efforts := item.ReasoningEfforts
		if len(efforts) == 0 {
			efforts = fallbackEfforts
		}
		if len(efforts) == 0 {
			efforts = []string{"auto"}
		} else if !containsString(efforts, "auto") {
			// Auto is Lite2API's delegation value: omit explicit reasoning and let
			// the selected upstream use its own default.
			efforts = append([]string{"auto"}, efforts...)
		}
		capability := ChannelCapability{
			Model:            logicalModel,
			UpstreamModel:    item.ID,
			ReasoningEfforts: unionStrings(nil, efforts),
		}
		capabilities = append(capabilities, capability)
		if discoveredFastAvailable(account, capability, item.ServiceTiers) {
			fast := capability
			fast.Model = FastProfileModel(logicalModel)
			fast.ReasoningEfforts = append([]string(nil), capability.ReasoningEfforts...)
			capabilities = append(capabilities, fast)
		}
	}
	return coalesceCapabilities(capabilities)
}

func inferDiscoveredLogicalModel(account Account, upstreamModel string) (string, []string) {
	if isCodexModelID(upstreamModel) {
		logical := codexLogicalModel(upstreamModel)
		efforts := codexDefaultReasoningEfforts
		if supportsGPT56Reasoning(upstreamModel) {
			efforts = codexGPT56ReasoningEfforts
		}
		return logical, append([]string(nil), efforts...)
	}
	return inferGenericDiscoveredCapability(account, upstreamModel)
}

func discoveryScope(account Account) string {
	if !strings.EqualFold(strings.TrimSpace(account.AdapterID), "cli-proxy-api") {
		return ""
	}
	// Only stable account identity determines scope. Discovered model names are
	// deliberately excluded: an aggregate pool eventually contains every family
	// and must remain aggregate on subsequent refreshes.
	text := strings.ToLower(strings.Join([]string{account.ID, account.Name, account.InstanceID}, " "))
	switch {
	case strings.Contains(text, "claude-code"):
		return "claude-code"
	case strings.Contains(text, "antigravity"):
		return "antigravity"
	case strings.Contains(text, "gemini"):
		return "gemini"
	case strings.Contains(text, "codex") || strings.Contains(text, "openai"):
		return "codex"
	default:
		return ""
	}
}

func modelMatchesDiscoveryScope(scope, model string) bool {
	if scope == "" {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch scope {
	case "claude-code":
		return strings.HasPrefix(normalized, "claude-code/")
	case "antigravity":
		return strings.HasPrefix(normalized, "antigravity/")
	case "gemini":
		return strings.Contains(normalized, "gemini") && !strings.HasPrefix(normalized, "antigravity/")
	case "codex":
		return isCodexModelID(normalized)
	default:
		return true
	}
}

func inferGenericDiscoveredCapability(_ Account, upstreamModel string) (string, []string) {
	normalized := strings.ToLower(strings.TrimSpace(upstreamModel))
	logicalModel := strings.TrimSpace(upstreamModel)
	efforts := []string{"auto"}

	switch {
	case strings.HasPrefix(normalized, "claude-code/"):
		logicalModel = strings.TrimSpace(upstreamModel[len("claude-code/"):])
	case strings.HasPrefix(normalized, "antigravity/"):
		logicalModel = strings.TrimSpace(upstreamModel[len("antigravity/"):])
		if strings.HasSuffix(strings.ToLower(logicalModel), "-thinking") {
			logicalModel = logicalModel[:len(logicalModel)-len("-thinking")]
			efforts = []string{"high"}
		}
	case strings.Contains(normalized, "gemini") && strings.HasSuffix(normalized, "-thinking"):
		logicalModel = upstreamModel[:len(upstreamModel)-len("-thinking")]
		efforts = []string{"high"}
	}
	if logicalModel == "" {
		return "", nil
	}
	return logicalModel, efforts
}

func normalizeDiscoveredReasoning(values []string) []string {
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value == "" || !ValidReasoningEffort(value) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func normalizeDiscoveredTiers(values []string) []string {
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		switch value {
		case "fast", "priority":
			result = append(result, value)
		}
	}
	return result
}

func discoveredFastAvailable(account Account, capability ChannelCapability, tiers []string) bool {
	for _, tier := range tiers {
		if tier == "fast" || tier == "priority" {
			return true
		}
	}
	// The public OpenAI /v1/models response intentionally omits service-tier
	// metadata. Restrict this fallback to the official API host and currently
	// documented GPT-5.6 family rather than guessing for third-party proxies.
	if !isOfficialOpenAIAPI(account.BaseURL) {
		return false
	}
	switch normalizeCodexModelID(capability.UpstreamModel) {
	case "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna":
		return true
	default:
		return false
	}
}

func isOfficialOpenAIAPI(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	return err == nil && strings.EqualFold(parsed.Hostname(), "api.openai.com")
}

func coalesceCapabilities(values []ChannelCapability) []ChannelCapability {
	result := make([]ChannelCapability, 0, len(values))
	byModel := make(map[string]int, len(values))
	for _, capability := range values {
		capability.Model = strings.TrimSpace(capability.Model)
		capability.UpstreamModel = strings.TrimSpace(capability.UpstreamModel)
		if capability.Model == "" || capability.UpstreamModel == "" {
			continue
		}
		if index, exists := byModel[capability.Model]; exists {
			current := result[index]
			current.ReasoningEfforts = unionStrings(current.ReasoningEfforts, capability.ReasoningEfforts)
			if codexModelPreference(capability.UpstreamModel) > codexModelPreference(current.UpstreamModel) ||
				(strings.Contains(capability.UpstreamModel, "/") && !strings.Contains(current.UpstreamModel, "/")) {
				current.UpstreamModel = capability.UpstreamModel
			}
			result[index] = current
			continue
		}
		capability.ReasoningEfforts = unionStrings(nil, capability.ReasoningEfforts)
		byModel[capability.Model] = len(result)
		result = append(result, capability)
	}
	return result
}
