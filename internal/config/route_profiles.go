package config

import "strings"

const fastProfileSuffix = "@fast"

// FastProfileModel returns the logical routing profile used to keep request
// service tier separate from the concrete upstream model ID. The profile is an
// internal logical model; ResolveRouteTarget still maps it to the same concrete
// model as the standard profile.
func FastProfileModel(model string) string {
	base, _ := ParseRouteModelProfile(model)
	if base == "" {
		return ""
	}
	return base + fastProfileSuffix
}

// ParseRouteModelProfile separates an internal route profile from its base
// logical model. Additional execution profiles can be added here without
// changing the route JSON schema.
func ParseRouteModelProfile(model string) (base string, fast bool) {
	model = strings.TrimSpace(model)
	if strings.HasSuffix(strings.ToLower(model), fastProfileSuffix) {
		return strings.TrimSpace(model[:len(model)-len(fastProfileSuffix)]), true
	}
	return model, false
}

// ExpandFastProfiles adds request-level Fast variants only where the concrete
// upstream is known to support the current OpenAI Fast service tier. The
// concrete upstream model and reasoning matrix remain unchanged.
func ExpandFastProfiles(account Account, capabilities []ChannelCapability) []ChannelCapability {
	result := append([]ChannelCapability(nil), capabilities...)
	seen := make(map[string]struct{}, len(result)*2)
	for _, capability := range result {
		seen[capability.Model] = struct{}{}
	}
	for _, capability := range capabilities {
		if !supportsFastServiceTier(account, capability) {
			continue
		}
		profile := capability
		profile.Model = FastProfileModel(capability.Model)
		profile.ReasoningEfforts = append([]string(nil), capability.ReasoningEfforts...)
		if profile.Model == "" {
			continue
		}
		if _, exists := seen[profile.Model]; exists {
			continue
		}
		seen[profile.Model] = struct{}{}
		result = append(result, profile)
	}
	return result
}

func supportsFastServiceTier(account Account, capability ChannelCapability) bool {
	if !strings.EqualFold(strings.TrimSpace(account.Type), "openai") {
		return false
	}
	normalized := normalizeCodexModelID(capability.UpstreamModel)
	switch normalized {
	case "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "sol", "terra":
		return true
	default:
		return false
	}
}
