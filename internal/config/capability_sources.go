package config

import "strings"

// UseRichCodexSupplement reports whether an aggregate CLIProxy connection
// should keep its full /models catalog and additionally merge the Codex rich
// catalog. This is the common OAuth-pool shape created by Lite2API itself.
func UseRichCodexSupplement(account Account) bool {
	return strings.EqualFold(strings.TrimSpace(account.AdapterID), "cli-proxy-api") && discoveryScope(account) == ""
}
