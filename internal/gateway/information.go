package gateway

import (
	"errors"
	"net/url"
	"strings"

	"github.com/lms2004/lite2api/internal/config"
)

const secretPlaceholder = "********"

// URLs can carry credentials outside api_key and custom headers. Management
// snapshots need the destination, not query values or proxy user information.
func redactedAccountURL(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[invalid URL]"
	}
	if parsed.User != nil {
		parsed.User = url.UserPassword(secretPlaceholder, secretPlaceholder)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		parsed.RawQuery = ""
	} else if len(query) > 0 {
		for key, values := range query {
			for i := range values {
				values[i] = secretPlaceholder
			}
			query[key] = values
		}
		parsed.RawQuery = query.Encode()
	}
	parsed.Fragment = ""
	return parsed.String()
}

// Restore only an unchanged redacted URL. Editing a masked URL requires a
// complete replacement so a placeholder cannot silently become a credential.
func restoreAccountDisplaySecrets(account *config.Account, existing *config.Account) error {
	if account.APIKey == secretPlaceholder {
		if existing == nil {
			return errors.New("a new account requires the actual API key")
		}
		account.APIKey = existing.APIKey
	}
	for name, value := range account.Headers {
		if value != secretPlaceholder {
			continue
		}
		if existing == nil || existing.Headers[name] == "" {
			return errors.New("a masked header requires an existing value")
		}
		account.Headers[name] = existing.Headers[name]
	}
	for _, field := range []struct {
		value *string
		old   string
	}{
		{&account.BaseURL, existingURL(existing, false)},
		{&account.ProxyURL, existingURL(existing, true)},
	} {
		if existing != nil && *field.value == redactedAccountURL(field.old) {
			*field.value = field.old
			continue
		}
		decoded, _ := url.QueryUnescape(*field.value)
		if strings.Contains(decoded, secretPlaceholder) {
			return errors.New("replace an edited masked URL with its complete value")
		}
	}
	return nil
}

func existingURL(account *config.Account, proxy bool) string {
	if account == nil {
		return ""
	}
	if proxy {
		return account.ProxyURL
	}
	return account.BaseURL
}
