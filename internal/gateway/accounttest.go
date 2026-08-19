package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

const (
	accountTestTimeout      = 12 * time.Second
	accountTestResponseSize = 2 << 20
)

type accountTestRequest struct {
	Account config.Account `json:"account"`
}

type accountTestResult struct {
	OK          bool     `json:"ok"`
	Status      int      `json:"status"`
	LatencyMS   int64    `json:"latency_ms"`
	Endpoint    string   `json:"endpoint"`
	ModelCount  int      `json:"model_count"`
	Models      []string `json:"models,omitempty"`
	ContentType string   `json:"content_type,omitempty"`
	Message     string   `json:"message"`
}

func (g *Gateway) serveAccountTest(w http.ResponseWriter, r *http.Request, state *runtimeState) {
	var input accountTestRequest
	if err := decodeAdminJSON(w, r, &input); err != nil {
		return
	}
	account := input.Account
	account.ID = strings.TrimSpace(account.ID)
	if account.ID == "" {
		account.ID = "connection-test"
	}
	if account.APIKey == "********" {
		account.APIKey = ""
	}
	if existing := configuredAccountByID(state.cfg.Accounts, account.ID); existing != nil {
		if account.APIKey == "" && account.APIKeyEnv == "" {
			account.APIKey, account.APIKeyEnv = existing.APIKey, existing.APIKeyEnv
		}
		for name, value := range account.Headers {
			if value == "********" {
				account.Headers[name] = existing.Headers[name]
			}
		}
	}

	probeConfig := config.Normalize(config.Config{
		Server:   state.cfg.Server,
		Accounts: []config.Account{account},
		Routes:   map[string]config.Route{},
	})
	if err := probeConfig.Validate(); err != nil {
		writeAPIErrorCode(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "invalid_account_test_config")
		return
	}
	account = probeConfig.Accounts[0]
	if account.AuthHeader != "none" && strings.TrimSpace(account.ResolvedAPIKey()) == "" {
		writeAPIErrorCode(w, http.StatusBadRequest, "API key is empty or its environment variable is not available", "invalid_request_error", "account_test_missing_credential")
		return
	}

	result, err := probeAccountModels(r.Context(), account)
	if err != nil {
		writeAPIErrorCode(w, http.StatusBadGateway, err.Error(), "upstream_error", "account_test_failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func configuredAccountByID(accounts []config.Account, id string) *config.Account {
	for index := range accounts {
		if accounts[index].ID == id {
			copy := accounts[index]
			return &copy
		}
	}
	return nil
}

func probeAccountModels(parent context.Context, account config.Account) (accountTestResult, error) {
	base, err := url.Parse(strings.TrimRight(account.BaseURL, "/"))
	if err != nil {
		return accountTestResult{}, fmt.Errorf("parse account base URL: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/models"
	base.RawQuery = ""
	base.Fragment = ""

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(account.ProxyURL) != "" {
		proxy, err := url.Parse(account.ProxyURL)
		if err != nil {
			return accountTestResult{}, fmt.Errorf("parse proxy URL: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	client := &http.Client{Transport: transport, Timeout: accountTestTimeout}
	ctx, cancel := context.WithTimeout(parent, accountTestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return accountTestResult{}, fmt.Errorf("create account test request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Lite2API-Account-Test/1.0")
	for name, value := range account.ResolvedHeaders() {
		if strings.TrimSpace(value) != "" {
			req.Header.Set(name, value)
		}
	}
	if account.AuthHeader != "none" {
		header := strings.TrimSpace(account.AuthHeader)
		if header == "" {
			if account.Type == "anthropic" {
				header = "x-api-key"
			} else {
				header = "Authorization"
			}
		}
		value := strings.TrimSpace(account.ResolvedAPIKey())
		if scheme := strings.TrimSpace(account.AuthScheme); scheme != "" {
			value = scheme + " " + value
		}
		req.Header.Set(header, value)
	}
	if account.Type == "anthropic" && req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	started := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return accountTestResult{}, errors.New("upstream connection test timed out after 12 seconds")
		}
		return accountTestResult{}, fmt.Errorf("upstream connection test failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, accountTestResponseSize+1))
	if err != nil {
		return accountTestResult{}, fmt.Errorf("read upstream test response: %w", err)
	}
	if len(body) > accountTestResponseSize {
		return accountTestResult{}, errors.New("upstream test response exceeded 2 MiB")
	}

	result := accountTestResult{
		OK:          resp.StatusCode >= 200 && resp.StatusCode < 300,
		Status:      resp.StatusCode,
		LatencyMS:   latency,
		Endpoint:    base.String(),
		ContentType: resp.Header.Get("Content-Type"),
		Message:     resp.Status,
	}
	if result.OK {
		result.Models = parseModelIDs(body)
		result.ModelCount = len(result.Models)
		if result.ModelCount > 0 {
			result.Message = fmt.Sprintf("connection succeeded; discovered %d models", result.ModelCount)
		} else {
			result.Message = "connection succeeded; the upstream did not expose a recognizable model catalog"
		}
	}
	return result, nil
}

func parseModelIDs(body []byte) []string {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	seen := map[string]struct{}{}
	models := make([]string, 0, len(payload.Data)+len(payload.Models))
	appendModel := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		models = append(models, value)
	}
	for _, item := range payload.Data {
		appendModel(item.ID)
	}
	for _, item := range payload.Models {
		if item.ID != "" {
			appendModel(item.ID)
		} else {
			appendModel(item.Name)
		}
	}
	sort.Strings(models)
	if len(models) > 24 {
		models = models[:24]
	}
	return models
}
