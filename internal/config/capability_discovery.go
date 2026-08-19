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

func InferDiscoveredCapabilities(account Account, models []string) []ChannelCapability {
	catalog := make([]DiscoveredModel, 0, len(models))
	for _, model := range models {
		catalog = append(catalog, DiscoveredModel{ID: model})
	}
	return InferDiscoveredModelCapabilities(account, catalog)
}

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
		} else if !discoveredContains(efforts, "auto") {
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
		// CLIProxy's shared /models endpoint can include Antigravity's
		// unqualified gpt-oss entry alongside the real Codex catalog. It is
		// not safe to let a Codex-scoped account claim that model: doing so
		// merges two different upstreams into one picker entry.
		return !strings.HasPrefix(normalized, "gpt-oss-") && isCodexModelID(normalized)
	default:
		return true
	}
}

func inferGenericDiscoveredCapability(_ Account, upstreamModel string) (string, []string) {
	normalized := strings.ToLower(strings.TrimSpace(upstreamModel))
	// Fast is an execution tier/profile, never a concrete model. Some compatible
	// catalogs still expose a historical fast slug; ignore it to avoid showing
	// both a fake model and the real @fast profile.
	if normalized == "fast" || normalized == "gpt-5.6-fast" {
		return "", nil
	}
	logicalModel := strings.TrimSpace(upstreamModel)
	efforts := []string{"auto"}

	switch {
	case strings.HasPrefix(normalized, "claude-code/"):
		logicalModel = strings.TrimSpace(upstreamModel[len("claude-code/"):])
	case strings.HasPrefix(normalized, "antigravity/"):
		logicalModel = strings.TrimSpace(upstreamModel[len("antigravity/"):])
	case strings.Contains(normalized, "gemini") && strings.HasSuffix(normalized, "-thinking"):
		logicalModel = upstreamModel[:len(upstreamModel)-len("-thinking")]
		efforts = []string{"high"}
	}
	if base, effort, ok := stripReasoningSuffix(logicalModel); ok {
		logicalModel = base
		efforts = []string{effort}
	}
	if logicalModel == "" {
		return "", nil
	}
	return logicalModel, efforts
}

func stripReasoningSuffix(model string) (string, string, bool) {
	value := strings.TrimSpace(model)
	normalized := strings.ToLower(value)
	for _, item := range []struct{ suffix, effort string }{
		{suffix: "-extra-low", effort: "low"},
		{suffix: "-minimal", effort: "minimal"},
		{suffix: "-medium", effort: "medium"},
		{suffix: "-thinking", effort: "high"},
		{suffix: "-high", effort: "high"},
		{suffix: "-xhigh", effort: "xhigh"},
		{suffix: "-ultra", effort: "ultra"},
		{suffix: "-max", effort: "max"},
		{suffix: "-low", effort: "low"},
		{suffix: "-none", effort: "none"},
	} {
		if strings.HasSuffix(normalized, item.suffix) && len(value) > len(item.suffix) {
			return value[:len(value)-len(item.suffix)], item.effort, true
		}
	}
	return value, "", false
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

func discoveredContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
