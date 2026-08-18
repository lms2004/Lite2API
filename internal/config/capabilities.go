package config

import "strings"

var (
	// Codex exposes these as one model family with an independently selectable
	// reasoning effort. Keep auto as an internal delegation value: the gateway
	// omits reasoning_effort in that case and lets the client/upstream decide.
	codexGPT56ReasoningEfforts   = []string{"auto", "none", "low", "medium", "high", "xhigh", "max"}
	codexDefaultReasoningEfforts = []string{"auto"}
)

// InferCodexCapabilities converts the concrete model IDs returned by a Codex
// compatible upstream into the logical model/capability records used by the
// routing UI and scheduler.
//
// The upstream model ID is always preserved. Only well-known GPT-5.6 names
// receive the full reasoning matrix; unknown Codex IDs remain routable with
// auto delegation instead of claiming unsupported effort values.
func InferCodexCapabilities(models []string) []ChannelCapability {
	capabilities := make([]ChannelCapability, 0, len(models))
	seenLogicalModels := make(map[string]struct{}, len(models))
	for _, raw := range models {
		upstreamModel := strings.TrimSpace(raw)
		if upstreamModel == "" || upstreamModel == "*" || !isCodexModelID(upstreamModel) {
			continue
		}
		logicalModel := codexLogicalModel(upstreamModel)
		if logicalModel == "" {
			continue
		}
		if _, exists := seenLogicalModels[logicalModel]; exists {
			continue
		}
		seenLogicalModels[logicalModel] = struct{}{}
		efforts := codexDefaultReasoningEfforts
		if supportsGPT56Reasoning(upstreamModel) {
			efforts = codexGPT56ReasoningEfforts
		}
		capabilities = append(capabilities, ChannelCapability{
			Model:            logicalModel,
			UpstreamModel:    upstreamModel,
			ReasoningEfforts: append([]string(nil), efforts...),
		})
	}
	return capabilities
}

func isCodexModelID(model string) bool {
	normalized := normalizeCodexModelID(model)
	switch normalized {
	case "sol", "luna", "terra", "fast", "sol-max":
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
	case "gpt-5.6-fast", "fast":
		return "fast"
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
