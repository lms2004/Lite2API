package config

import "strings"

var (
	// Codex exposes these as one model family with an independently selectable
	// reasoning effort. Keep auto as an internal delegation value: the gateway
	// omits reasoning_effort in that case and lets the client/upstream decide.
	codexGPT56ReasoningEfforts   = []string{"auto", "none", "low", "medium", "high", "xhigh", "max"}
	codexDefaultReasoningEfforts = []string{"auto"}
)

// InferCodexCapabilities converts concrete model IDs returned by a Codex
// compatible upstream into logical model/capability records used by routing.
//
// Discovery endpoints sometimes return both a short alias and its canonical
// model ID. In that case keep one logical capability and prefer the canonical
// ID, while preserving the richest reasoning-effort set we observed.
func InferCodexCapabilities(models []string) []ChannelCapability {
	byLogical := make(map[string]ChannelCapability, len(models))
	order := make([]string, 0, len(models))
	for _, raw := range models {
		upstreamModel := strings.TrimSpace(raw)
		if upstreamModel == "" || upstreamModel == "*" || !isCodexModelID(upstreamModel) {
			continue
		}
		logicalModel := codexLogicalModel(upstreamModel)
		if logicalModel == "" {
			continue
		}
		efforts := codexDefaultReasoningEfforts
		if supportsGPT56Reasoning(upstreamModel) {
			efforts = codexGPT56ReasoningEfforts
		}
		candidate := ChannelCapability{
			Model:            logicalModel,
			UpstreamModel:    upstreamModel,
			ReasoningEfforts: append([]string(nil), efforts...),
		}
		current, exists := byLogical[logicalModel]
		if !exists {
			byLogical[logicalModel] = candidate
			order = append(order, logicalModel)
			continue
		}
		current.ReasoningEfforts = unionStrings(current.ReasoningEfforts, candidate.ReasoningEfforts)
		if codexModelPreference(candidate.UpstreamModel) > codexModelPreference(current.UpstreamModel) {
			current.UpstreamModel = candidate.UpstreamModel
		}
		byLogical[logicalModel] = current
	}
	capabilities := make([]ChannelCapability, 0, len(order))
	for _, logicalModel := range order {
		capabilities = append(capabilities, byLogical[logicalModel])
	}
	return capabilities
}

func isCodexModelID(model string) bool {
	normalized := normalizeCodexModelID(model)
	if normalized == "fast" || normalized == "gpt-5.6-fast" {
		return false
	}
	switch normalized {
	case "sol", "luna", "terra", "sol-max":
		return true
	default:
		return strings.HasPrefix(normalized, "gpt-") || strings.Contains(normalized, "codex")
	}
}

func codexLogicalModel(upstreamModel string) string {
	switch normalized := normalizeCodexModelID(upstreamModel); normalized {
	case "gpt-5.6", "gpt-5.6-sol", "sol":
		return "sol"
	case "gpt-5.6-luna", "luna":
		return "luna"
	case "gpt-5.6-terra", "terra":
		return "terra"
	case "gpt-5.6-sol-max", "sol-max":
		return "sol-max"
	default:
		return upstreamModel
	}
}

func normalizeCodexModelID(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"openai-codex/", "openai/", "codex/"} {
		normalized = strings.TrimPrefix(normalized, prefix)
	}
	return normalized
}

func supportsGPT56Reasoning(model string) bool {
	switch normalizeCodexModelID(model) {
	case "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.6-terra", "sol", "luna", "terra":
		return true
	default:
		return false
	}
}

func codexModelPreference(model string) int {
	switch normalizeCodexModelID(model) {
	case "gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.6-terra", "gpt-5.6-sol-max":
		return 3
	case "gpt-5.6":
		return 2
	case "sol", "luna", "terra", "sol-max":
		return 1
	default:
		return 2
	}
}

func unionStrings(left, right []string) []string {
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
