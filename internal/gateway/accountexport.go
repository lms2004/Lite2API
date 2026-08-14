package gateway

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

type AccountExportRequest struct {
	IDs            []string `json:"ids,omitempty"`
	IncludeProxies bool     `json:"include_proxies,omitempty"`
}

func ExportAccounts(cfg config.Config, request AccountExportRequest) (AccountImportData, error) {
	selected := make(map[string]struct{}, len(request.IDs))
	for _, rawID := range request.IDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return AccountImportData{}, errors.New("export account id cannot be empty")
		}
		if _, duplicate := selected[id]; duplicate {
			continue
		}
		selected[id] = struct{}{}
	}

	accounts := make([]config.Account, 0, len(cfg.Accounts))
	found := make(map[string]struct{}, len(selected))
	for _, account := range cfg.Accounts {
		if len(selected) > 0 {
			if _, ok := selected[account.ID]; !ok {
				continue
			}
			found[account.ID] = struct{}{}
		}
		accounts = append(accounts, account)
	}
	if len(selected) > 0 && len(found) != len(selected) {
		missing := make([]string, 0)
		for id := range selected {
			if _, ok := found[id]; !ok {
				missing = append(missing, id)
			}
		}
		return AccountImportData{}, fmt.Errorf("export accounts not found: %s", strings.Join(missing, ", "))
	}
	if len(accounts) == 0 {
		return AccountImportData{}, errors.New("no accounts selected for export")
	}

	data := AccountImportData{
		Type:       "lite2api-data",
		Version:    accountImportVersion,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Accounts:   make([]AccountImportItem, 0, len(accounts)),
	}
	proxyKeys := make(map[string]string)
	for _, account := range accounts {
		item := AccountImportItem{
			ID: account.ID, Name: account.Name, Type: account.Type, BaseURL: account.BaseURL,
			APIKey: account.APIKey, APIKeyEnv: account.APIKeyEnv,
			AuthHeader: account.AuthHeader, AuthScheme: account.AuthScheme,
			Headers: cloneStringMap(account.Headers), HeadersEnv: cloneStringMap(account.HeadersEnv),
			Models: append([]string(nil), account.Models...), ModelMap: cloneStringMap(account.ModelMap),
			Priority: account.Priority, Weight: account.Weight, Concurrency: account.Concurrency,
		}
		enabled := account.Enabled
		item.Enabled = &enabled
		if request.IncludeProxies && strings.TrimSpace(account.ProxyURL) != "" {
			proxyURL := strings.TrimSpace(account.ProxyURL)
			key, ok := proxyKeys[proxyURL]
			if !ok {
				proxy, err := exportProxy(proxyURL, len(proxyKeys)+1)
				if err != nil {
					return AccountImportData{}, fmt.Errorf("account %q proxy: %w", account.ID, err)
				}
				key = proxy.ProxyKey
				proxyKeys[proxyURL] = key
				data.Proxies = append(data.Proxies, proxy)
			}
			item.ProxyKey = &key
		}
		data.Accounts = append(data.Accounts, item)
	}
	return data, nil
}

func exportProxy(rawURL string, index int) (AccountImportProxy, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return AccountImportProxy{}, errors.New("invalid proxy URL")
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" && scheme != "socks5" && scheme != "socks5h" {
		return AccountImportProxy{}, fmt.Errorf("unsupported proxy protocol %q", parsed.Scheme)
	}
	host := strings.TrimSpace(parsed.Hostname())
	portText := strings.TrimSpace(parsed.Port())
	if host == "" || portText == "" {
		return AccountImportProxy{}, errors.New("proxy URL must include host and port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return AccountImportProxy{}, errors.New("proxy URL port is invalid")
	}
	if net.ParseIP(host) == nil && strings.ContainsAny(host, " /?#") {
		return AccountImportProxy{}, errors.New("proxy URL host is invalid")
	}
	proxy := AccountImportProxy{ProxyKey: fmt.Sprintf("lite2api-proxy-%d", index), Protocol: scheme, Host: host, Port: port}
	if parsed.User != nil {
		proxy.Username = parsed.User.Username()
		proxy.Password, _ = parsed.User.Password()
	}
	return proxy, nil
}
