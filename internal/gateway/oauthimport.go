package gateway

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// OAuth account import for the Sub2API format.
//
// Sub2API exports carry two credential families. API-key accounts become
// normal Lite2API upstream channels via AccountImportItem.toAccount. OAuth /
// cookie accounts must never be guessed into an upstream API key; instead the
// raw token bundle is pushed to the isolated CLIProxyAPI pool over the loopback
// management channel (POST /v0/management/auth-files). The bundle shapes here
// mirror deploy/migrate-sub2api-auths.mjs so the in-app path and the offline
// migration script produce byte-compatible credential files.

// errOAuthPlatformNotPooled marks an OAuth account whose platform CLIProxyAPI
// does not host as a pooled auth-file (e.g. grok/xai). Such accounts require the
// offline migration path and are surfaced to the operator rather than dropped.
var errOAuthPlatformNotPooled = errors.New("platform is not hosted by the CLIProxy OAuth pool; use the offline auth-files migration")

var oauthAuthFileNameUnsafe = regexp.MustCompile(`[^a-z0-9@._-]+`)

// isOAuthImportItem reports whether an import item is an OAuth/cookie account
// that must go to the CLIProxy pool instead of becoming an API-key channel. It
// intentionally runs after the API-key checks: an account that carries a usable
// api_key keeps the existing channel behaviour untouched.
func (item AccountImportItem) isOAuthImportItem() bool {
	if firstNonEmpty(item.APIKey, stringValue(item.Credentials, "api_key"), item.APIKeyEnv, stringValue(item.Extra, "api_key_env")) != "" {
		return false
	}
	importType := strings.ToLower(strings.TrimSpace(item.Type))
	if importType == "oauth" || importType == "cookie" {
		return true
	}
	return stringValue(item.Credentials, "access_token") != "" || stringValue(item.Credentials, "refresh_token") != ""
}

// buildOAuthAuthFile maps a Sub2API OAuth account to a CLIProxy provider, the
// destination auth-file name, and the credential bundle to upload. It returns
// errOAuthPlatformNotPooled for platforms CLIProxy does not pool.
func (item AccountImportItem) buildOAuthAuthFile(index int) (provider, fileName string, bundle map[string]any, err error) {
	platform := strings.ToLower(strings.TrimSpace(item.Platform))
	credentials := item.Credentials
	accessToken := stringValue(credentials, "access_token")
	refreshToken := stringValue(credentials, "refresh_token")
	if accessToken == "" && refreshToken == "" {
		return "", "", nil, errors.New("OAuth account has no access_token or refresh_token")
	}

	label := firstNonEmpty(item.Name, credString(credentials, "email", "email_address"), fmt.Sprintf("%s-%d", platform, index+1))
	email := credString(credentials, "email", "email_address")
	if email == "" && strings.Contains(label, "@") {
		email = label
	}
	if email == "" {
		email = stringValue(item.Extra, "email")
	}
	expired := normalizeOAuthExpiry(credAny(credentials, "expires_at", "expired"))
	now := time.Now().UTC().Format(time.RFC3339)

	switch platform {
	case "gemini":
		projectID := credString(credentials, "project_id")
		token := map[string]any{
			"access_token":  accessToken,
			"refresh_token": refreshToken,
			"token_type":    firstNonEmpty(credString(credentials, "token_type"), "Bearer"),
			"expiry":        expired,
		}
		bundle = map[string]any{
			"type": "gemini", "email": email, "project_id": projectID,
			"auto": false, "checked": false, "token": token,
		}
		return "gemini", fmt.Sprintf("gemini-%s-%s.json", safeOAuthName(firstNonEmpty(email, label)), safeOAuthName(firstNonEmpty(projectID, strconv.Itoa(index+1)))), bundle, nil
	case "anthropic", "claude":
		bundle = map[string]any{
			"type": "claude", "access_token": accessToken, "refresh_token": refreshToken, "email": email,
			"account_uuid":      credString(credentials, "account_uuid"),
			"organization_uuid": credString(credentials, "org_uuid", "organization_uuid"),
			"last_refresh":      now, "expired": expired,
		}
		return "claude", fmt.Sprintf("claude-%s.json", safeOAuthName(firstNonEmpty(email, label))), bundle, nil
	case "antigravity":
		bundle = map[string]any{
			"type": "antigravity", "access_token": accessToken, "refresh_token": refreshToken, "email": email,
			"project_id": credString(credentials, "project_id"),
			"token_type": firstNonEmpty(credString(credentials, "token_type"), "Bearer"),
			"expired":    expired,
		}
		return "antigravity", fmt.Sprintf("antigravity-%s.json", safeOAuthName(firstNonEmpty(email, label))), bundle, nil
	case "openai", "codex":
		bundle = map[string]any{
			"type": "codex", "access_token": accessToken, "refresh_token": refreshToken,
			"id_token": credString(credentials, "id_token"), "email": email,
			"account_id":   credString(credentials, "chatgpt_account_id", "account_id"),
			"last_refresh": now, "expired": expired,
		}
		return "codex", fmt.Sprintf("codex-%s.json", safeOAuthName(firstNonEmpty(email, label))), bundle, nil
	default:
		// grok/xai and anything else CLIProxy does not pool as an auth-file.
		return "", "", nil, errOAuthPlatformNotPooled
	}
}

// credString returns the first non-empty string credential among keys, coercing
// non-string JSON scalars (numbers, bools) to their string form.
func credString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := coerceScalar(values[key]); text != "" {
			return text
		}
	}
	return ""
}

// credAny returns the first present, non-empty raw credential among keys.
func credAny(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok && coerceScalar(value) != "" {
			return value
		}
	}
	return nil
}

func coerceScalar(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(v, 'f', -1, 64))
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

// normalizeOAuthExpiry mirrors the migration script's expiry(): epoch seconds or
// milliseconds and RFC3339-ish strings all normalize to RFC3339; empty stays "".
func normalizeOAuthExpiry(value any) string {
	text := coerceScalar(value)
	if text == "" {
		return ""
	}
	if number, err := strconv.ParseFloat(text, 64); err == nil {
		if number > 1e12 {
			number /= 1000
		}
		return time.Unix(int64(number), 0).UTC().Format(time.RFC3339)
	}
	if parsed, err := time.Parse(time.RFC3339, text); err == nil {
		return parsed.UTC().Format(time.RFC3339)
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999Z07:00", time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// safeOAuthName mirrors the migration script's safe(): lowercase, keep only
// [a-z0-9@._-], collapse runs, trim separators, cap length. Used for auth-file
// names so the in-app and offline paths land on identical files.
func safeOAuthName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = "account"
	}
	value = oauthAuthFileNameUnsafe.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if len(value) > 100 {
		value = value[:100]
	}
	if value == "" {
		return "account"
	}
	return value
}
