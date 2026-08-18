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
// changing the persisted route JSON schema.
func ParseRouteModelProfile(model string) (base string, fast bool) {
	model = strings.TrimSpace(model)
	if strings.HasSuffix(strings.ToLower(model), fastProfileSuffix) {
		return strings.TrimSpace(model[:len(model)-len(fastProfileSuffix)]), true
	}
	return model, false
}
