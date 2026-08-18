package config

import "strings"

// FilterDiscoveredModels scopes a shared adapter's /models response to the
// channel represented by account. Generic OpenAI-compatible accounts retain the
// full response. CLIProxy often exposes several credential families behind one
// endpoint, so a provider-specific account must not suddenly claim every model
// served by the shared process.
func FilterDiscoveredModels(account Account, models []string) []string {
	scope := discoveryScope(account)
	result := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, raw := range models {
		model := strings.TrimSpace(raw)
		if model == "" || model == "*" || !modelMatchesDiscoveryScope(scope, model) {
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

// InferDiscoveredCapabilities turns a live /models response into conservative
// routing capabilities. Rich manually configured capabilities are expected to
// be merged on top by the gateway; discovery never invents unsupported
// reasoning levels for unknown providers.
func InferDiscoveredCapabilities(account Account, models []string) []ChannelCapability {
	models = FilterDiscoveredModels(account, models)
	capabilities := make([]ChannelCapability, 0, len(models))
	consumed := make(map[string]struct{}, len(models))

	for _, capability := range InferCodexCapabilities(models) {
		capabilities = append(capabilities, capability)
		consumed[capability.UpstreamModel] = struct{}{}
	}

	for _, upstreamModel := range models {
		if _, exists := consumed[upstreamModel]; exists {
			continue
		}
		logicalModel, efforts := inferGenericDiscoveredCapability(account, upstreamModel)
		if logicalModel == "" {
			continue
		}
		capabilities = append(capabilities, ChannelCapability{
			Model:            logicalModel,
			UpstreamModel:    upstreamModel,
			ReasoningEfforts: efforts,
		})
	}
	return ExpandFastProfiles(account, coalesceCapabilities(capabilities))
}

func discoveryScope(account Account) string {
	if !strings.EqualFold(strings.TrimSpace(account.AdapterID), "cli-proxy-api") {
		return ""
	}
	// Only stable account identity determines scope. Discovered model names are
	// deliberately excluded: an aggregate pool eventually contains every family
	// and must remain aggregate on subsequent refreshes.
	text := strings.ToLower(strings.Join([]string{
		account.ID,
		account.Name,
		account.InstanceID,
	}, " "))
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

func inferGenericDiscoveredCapability(account Account, upstreamModel string) (string, []string) {
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
			// Prefer a provider-specific upstream ID over an unqualified fallback.
			if strings.Contains(capability.UpstreamModel, "/") && !strings.Contains(current.UpstreamModel, "/") {
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
