package gateway

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/lms2004/lite2api/internal/config"
)

const (
	accountImportVersion = 1
	accountImportLimit   = 500
)

type AccountImportData struct {
	Type       string               `json:"type,omitempty"`
	Version    int                  `json:"version,omitempty"`
	ExportedAt string               `json:"exported_at,omitempty"`
	Proxies    []AccountImportProxy `json:"proxies,omitempty"`
	Accounts   []AccountImportItem  `json:"accounts"`
}

type AccountImportProxy struct {
	ProxyKey string `json:"proxy_key,omitempty"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// AccountImportItem accepts the native Lite2API shape and the useful subset
// of a Sub2API account export. OAuth/cookie credentials deliberately stay in
// external adapters and are never guessed into an upstream API key.
type AccountImportItem struct {
	ID           string                     `json:"id,omitempty"`
	Name         string                     `json:"name,omitempty"`
	Platform     string                     `json:"platform,omitempty"`
	Type         string                     `json:"type,omitempty"`
	AdapterID    string                     `json:"adapter_id,omitempty"`
	InstanceID   string                     `json:"instance_id,omitempty"`
	BaseURL      string                     `json:"base_url,omitempty"`
	APIKey       string                     `json:"api_key,omitempty"`
	APIKeyEnv    string                     `json:"api_key_env,omitempty"`
	AuthHeader   string                     `json:"auth_header,omitempty"`
	AuthScheme   string                     `json:"auth_scheme,omitempty"`
	Headers      map[string]string          `json:"headers,omitempty"`
	HeadersEnv   map[string]string          `json:"headers_env,omitempty"`
	Models       []string                   `json:"models,omitempty"`
	ModelMap     map[string]string          `json:"model_map,omitempty"`
	Capabilities []config.ChannelCapability `json:"capabilities,omitempty"`
	Operations   []string                   `json:"operations,omitempty"`
	Priority     int                        `json:"priority,omitempty"`
	Weight       int                        `json:"weight,omitempty"`
	Concurrency  int                        `json:"concurrency,omitempty"`
	Enabled      *bool                      `json:"enabled,omitempty"`
	ProxyURL     string                     `json:"proxy_url,omitempty"`
	ProxyKey     *string                    `json:"proxy_key,omitempty"`
	Credentials  map[string]any             `json:"credentials,omitempty"`
	Extra        map[string]any             `json:"extra,omitempty"`
}

type AccountImportRequest struct {
	Data   AccountImportData `json:"data"`
	Mode   string            `json:"mode,omitempty"`
	DryRun bool              `json:"dry_run,omitempty"`
}

type AccountImportResult struct {
	ProxyCreated   int                  `json:"proxy_created"`
	ProxyReused    int                  `json:"proxy_reused"`
	ProxyFailed    int                  `json:"proxy_failed"`
	AccountCreated int                  `json:"account_created"`
	AccountUpdated int                  `json:"account_updated"`
	AccountSkipped int                  `json:"account_skipped"`
	AccountFailed  int                  `json:"account_failed"`
	Applied        bool                 `json:"applied"`
	SourceFormat   string               `json:"source_format"`
	Errors         []AccountImportError `json:"errors,omitempty"`
}

type AccountImportError struct {
	Kind      string `json:"kind"`
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	AdapterID string `json:"adapter_id,omitempty"`
	Message   string `json:"message"`
}

func (g *Gateway) ImportAccounts(request AccountImportRequest) (AccountImportResult, error) {
	result := AccountImportResult{SourceFormat: normalizedImportType(request.Data.Type)}
	if err := validateAccountImportHeader(request.Data); err != nil {
		return result, err
	}
	mode := strings.ToLower(strings.TrimSpace(request.Mode))
	if mode == "" {
		mode = "skip"
	}
	if mode != "skip" && mode != "upsert" {
		return result, errors.New("mode must be skip or upsert")
	}

	proxyURLs := validateImportProxies(request.Data.Proxies, &result)
	candidate := cloneConfig(g.state.Load().cfg)
	seen := make(map[string]struct{}, len(request.Data.Accounts))
	for index, item := range request.Data.Accounts {
		account, err := item.toAccount(index, proxyURLs)
		if err != nil {
			result.addError(index, item.ID, item.Name, err)
			continue
		}
		if _, duplicate := seen[account.ID]; duplicate {
			result.addError(index, account.ID, account.Name, errors.New("duplicate account id in import data"))
			continue
		}
		seen[account.ID] = struct{}{}

		existing := accountIndex(candidate.Accounts, account.ID)
		if existing >= 0 && mode == "skip" {
			result.AccountSkipped++
			continue
		}
		trial := cloneConfig(candidate)
		if existing >= 0 {
			if account.APIKey == "" && account.APIKeyEnv == "" {
				account.APIKey = trial.Accounts[existing].APIKey
				account.APIKeyEnv = trial.Accounts[existing].APIKeyEnv
			}
			trial.Accounts[existing] = account
		} else {
			trial.Accounts = append(trial.Accounts, account)
		}
		trial = config.Normalize(trial)
		if err := validateImportCandidate(trial, account.ID); err != nil {
			result.addError(index, account.ID, account.Name, err)
			continue
		}
		candidate = trial
		if existing >= 0 {
			result.AccountUpdated++
		} else {
			result.AccountCreated++
		}
	}
	if request.DryRun || result.AccountCreated+result.AccountUpdated == 0 {
		return result, nil
	}
	if err := g.saveAndReload(candidate); err != nil {
		return result, fmt.Errorf("apply account import: %w", err)
	}
	result.Applied = true
	return result, nil
}

func (r *AccountImportResult) addError(index int, id, name string, err error) {
	r.AccountFailed++
	r.Errors = append(r.Errors, AccountImportError{
		Kind: "account", Index: index, ID: id, Name: name, AdapterID: adapterForImport(name, err.Error()), Message: err.Error(),
	})
}

func (r *AccountImportResult) addProxyError(index int, id string, err error) {
	r.ProxyFailed++
	r.Errors = append(r.Errors, AccountImportError{
		Kind: "proxy", Index: index, ID: id, Message: err.Error(),
	})
}

func validateAccountImportHeader(data AccountImportData) error {
	switch normalizedImportType(data.Type) {
	case "lite2api-data", "sub2api-data", "sub2api-bundle":
	default:
		return fmt.Errorf("unsupported import type %q", data.Type)
	}
	if data.Version != 0 && data.Version != accountImportVersion {
		return fmt.Errorf("unsupported import version %d", data.Version)
	}
	if len(data.Accounts) == 0 {
		return errors.New("accounts cannot be empty")
	}
	if len(data.Accounts) > accountImportLimit {
		return fmt.Errorf("a single import cannot contain more than %d accounts", accountImportLimit)
	}
	return nil
}

func normalizedImportType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "lite2api-accounts" {
		return "lite2api-data"
	}
	return value
}

func (item AccountImportItem) toAccount(index int, proxyURLs map[string]string) (config.Account, error) {
	id := strings.TrimSpace(item.ID)
	if id == "" {
		id = stringValue(item.Extra, "lite2api_id")
	}
	if id == "" {
		id = importAccountID(item.Platform, item.Name, index)
	}
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = id
	}
	baseURL := firstNonEmpty(item.BaseURL, stringValue(item.Extra, "base_url"), stringValue(item.Credentials, "base_url"))
	apiKey := firstNonEmpty(item.APIKey, stringValue(item.Credentials, "api_key"))
	apiKeyEnv := firstNonEmpty(item.APIKeyEnv, stringValue(item.Extra, "api_key_env"))
	authHeader := firstNonEmpty(item.AuthHeader, stringValue(item.Extra, "auth_header"))

	importType := strings.ToLower(strings.TrimSpace(item.Type))
	if apiKey == "" && apiKeyEnv == "" && !strings.EqualFold(authHeader, "none") && importType != "" && importType != "openai" && importType != "anthropic" && importType != "api_key" {
		return config.Account{}, fmt.Errorf("account type %q requires an external adapter with base_url and API key", item.Type)
	}
	kind := importType
	if kind != "openai" && kind != "anthropic" {
		platform := strings.ToLower(strings.TrimSpace(item.Platform))
		if strings.Contains(platform, "anthropic") || strings.Contains(platform, "claude") {
			kind = "anthropic"
		} else {
			kind = "openai"
		}
	}
	models := append([]string(nil), item.Models...)
	if len(models) == 0 {
		models = stringSliceValue(item.Extra, "models")
	}
	modelMap := cloneStringMap(item.ModelMap)
	if len(modelMap) == 0 {
		modelMap = stringMapValue(item.Extra, "model_map")
	}
	if len(modelMap) == 0 {
		modelMap = stringMapValue(item.Credentials, "model_mapping")
	}
	enabled := true
	if item.Enabled != nil {
		enabled = *item.Enabled
	} else if value, ok := item.Extra["enabled"].(bool); ok {
		enabled = value
	}
	authScheme := firstNonEmpty(item.AuthScheme, stringValue(item.Extra, "auth_scheme"))
	proxyURL := strings.TrimSpace(item.ProxyURL)
	if proxyURL == "" && item.ProxyKey != nil {
		proxyKey := strings.TrimSpace(*item.ProxyKey)
		if proxyKey != "" {
			var found bool
			proxyURL, found = proxyURLs[proxyKey]
			if !found {
				return config.Account{}, fmt.Errorf("referenced proxy %q was not found or is invalid", proxyKey)
			}
		}
	}
	account := config.Account{
		ID: id, Name: name, Type: kind, AdapterID: strings.TrimSpace(item.AdapterID),
		InstanceID: strings.TrimSpace(item.InstanceID), BaseURL: baseURL,
		APIKey: apiKey, APIKeyEnv: apiKeyEnv, AuthHeader: authHeader, AuthScheme: authScheme,
		Headers: cloneStringMap(item.Headers), HeadersEnv: cloneStringMap(item.HeadersEnv),
		Models: models, ModelMap: modelMap, Capabilities: append([]config.ChannelCapability(nil), item.Capabilities...), Operations: append([]string(nil), item.Operations...),
		Priority: item.Priority, Weight: item.Weight,
		Concurrency: item.Concurrency, Enabled: enabled, ProxyURL: proxyURL,
	}
	if account.BaseURL == "" {
		return config.Account{}, errors.New("base_url is required; OAuth/cookie exports must use an external adapter")
	}
	return account, nil
}

func validateImportCandidate(candidate config.Config, id string) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	index := accountIndex(candidate.Accounts, id)
	if index < 0 {
		return errors.New("normalized account disappeared")
	}
	account := candidate.Accounts[index]
	if account.Enabled && !strings.EqualFold(account.AuthHeader, "none") && account.ResolvedAPIKey() == "" {
		return errors.New("enabled account requires api_key or a populated api_key_env")
	}
	return nil
}

func accountIndex(accounts []config.Account, id string) int {
	for index := range accounts {
		if accounts[index].ID == id {
			return index
		}
	}
	return -1
}

func validateImportProxies(proxies []AccountImportProxy, importResult *AccountImportResult) map[string]string {
	urls := make(map[string]string, len(proxies))
	for index, proxy := range proxies {
		scheme := strings.ToLower(strings.TrimSpace(proxy.Protocol))
		host := strings.TrimSpace(proxy.Host)
		key := strings.TrimSpace(proxy.ProxyKey)
		if key == "" {
			importResult.addProxyError(index, "", errors.New("proxy_key is required"))
			continue
		}
		if host == "" || proxy.Port < 1 || proxy.Port > 65535 {
			importResult.addProxyError(index, key, errors.New("proxy host and a port between 1 and 65535 are required"))
			continue
		}
		if scheme != "http" && scheme != "https" && scheme != "socks5" && scheme != "socks5h" {
			importResult.addProxyError(index, key, fmt.Errorf("unsupported proxy protocol %q", proxy.Protocol))
			continue
		}
		parsed := &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(proxy.Port))}
		if proxy.Username != "" {
			parsed.User = url.UserPassword(proxy.Username, proxy.Password)
		}
		proxyURL := parsed.String()
		if existing, ok := urls[key]; ok {
			if existing == proxyURL {
				importResult.ProxyReused++
			} else {
				importResult.addProxyError(index, key, errors.New("duplicate proxy_key has conflicting settings"))
			}
			continue
		}
		urls[key] = proxyURL
		importResult.ProxyCreated++
	}
	return urls
}

func importAccountID(platform, name string, index int) string {
	raw := strings.ToLower(strings.TrimSpace(platform + "-" + name))
	var builder strings.Builder
	lastDash := false
	for _, value := range raw {
		if unicode.IsLetter(value) || unicode.IsDigit(value) {
			if value <= unicode.MaxASCII {
				builder.WriteRune(value)
				lastDash = false
			}
			continue
		}
		if builder.Len() > 0 && !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	id := strings.Trim(builder.String(), "-")
	if id == "" {
		id = fmt.Sprintf("imported-account-%d", index+1)
	}
	if len(id) > 64 {
		id = strings.Trim(id[:64], "-")
	}
	return id
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func stringValue(values map[string]any, key string) string {
	if value, ok := values[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func stringSliceValue(values map[string]any, key string) []string {
	raw, ok := values[key].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, value := range raw {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}

func stringMapValue(values map[string]any, key string) map[string]string {
	raw, ok := values[key].(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]string, len(raw))
	for name, value := range raw {
		if text, ok := value.(string); ok {
			result[name] = text
		}
	}
	return result
}

func cloneStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func newAccountImportData(accounts []AccountImportItem) AccountImportData {
	return AccountImportData{
		Type: "lite2api-data", Version: accountImportVersion,
		ExportedAt: time.Now().UTC().Format(time.RFC3339), Accounts: accounts,
	}
}
