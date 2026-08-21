package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

const defaultOAuthAdapterURL = "http://127.0.0.1:45682"

var (
	errOAuthAdapterUnavailable = errors.New("OAuth adapter is not configured")
	oauthStatePattern          = regexp.MustCompile("^[A-Za-z0-9._~-]{1,256}$")
	oauthProviders             = map[string]struct {
		Endpoint         string
		CallbackRequired bool
	}{
		"codex":       {Endpoint: "codex-auth-url", CallbackRequired: true},
		"anthropic":   {Endpoint: "anthropic-auth-url", CallbackRequired: true},
		"gemini":      {Endpoint: "gemini-cli-auth-url", CallbackRequired: true},
		"antigravity": {Endpoint: "antigravity-auth-url", CallbackRequired: true},
		"kimi":        {Endpoint: "kimi-auth-url", CallbackRequired: false},
	}
)

type oauthStartRequest struct {
	Provider  string `json:"provider"`
	ProjectID string `json:"project_id,omitempty"`
}

type oauthCallbackInput struct {
	Provider    string `json:"provider"`
	RedirectURL string `json:"redirect_url"`
	State       string `json:"state"`
}

type oauthStatusInput struct {
	State    string `json:"state"`
	Provider string `json:"provider,omitempty"`
}

type oauthAccountStatusInput struct {
	ID       string `json:"id"`
	Disabled *bool  `json:"disabled"`
}

type oauthAccountDeleteInput struct {
	ID string `json:"id"`
}

type oauthAdapterResponse struct {
	Status string `json:"status"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Error  string `json:"error"`
}

type oauthCredential struct {
	ID             string             `json:"id"`
	Provider       string             `json:"provider"`
	Identity       string             `json:"identity"`
	Plan           string             `json:"plan,omitempty"`
	Status         string             `json:"status"`
	Disabled       bool               `json:"disabled"`
	Ready          bool               `json:"ready"`
	Success        int64              `json:"success"`
	Failed         int64              `json:"failed"`
	UpdatedAt      string             `json:"updated_at,omitempty"`
	LastRefresh    string             `json:"last_refresh,omitempty"`
	NextRetryAfter string             `json:"next_retry_after,omitempty"`
	QuotaExceeded  bool               `json:"quota_exceeded,omitempty"`
	QuotaWindows   []oauthQuotaWindow `json:"quota_windows"`
	PromptUsage    *oauthPromptUsage  `json:"prompt_usage,omitempty"`
}

type oauthPromptUsage struct {
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
	CachedTokens    int64  `json:"cached_tokens"`
	TotalTokens     int64  `json:"total_tokens"`
	ObservedAt      string `json:"observed_at"`
	Source          string `json:"source"`
	LatencyMS       int64  `json:"latency_ms,omitempty"`
}

type oauthQuotaWindow struct {
	Kind           string   `json:"kind"`
	Label          string   `json:"label,omitempty"`
	Model          string   `json:"model,omitempty"`
	UsedPercentage *float64 `json:"used_percentage,omitempty"`
	Remaining      *float64 `json:"remaining,omitempty"`
	Limit          *float64 `json:"limit,omitempty"`
	Unit           string   `json:"unit,omitempty"`
	Status         string   `json:"status,omitempty"`
	ResetAt        string   `json:"reset_at,omitempty"`
	ObservedAt     string   `json:"observed_at"`
	Source         string   `json:"source"`
}

type oauthAuthFilesResponse struct {
	Files []struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		AuthIndex      string `json:"auth_index"`
		Provider       string `json:"provider"`
		Label          string `json:"label"`
		Email          string `json:"email"`
		Account        string `json:"account"`
		AccountType    string `json:"account_type"`
		Status         string `json:"status"`
		Disabled       bool   `json:"disabled"`
		Unavailable    bool   `json:"unavailable"`
		Success        int64  `json:"success"`
		Failed         int64  `json:"failed"`
		UpdatedAt      string `json:"updated_at"`
		LastRefresh    string `json:"last_refresh"`
		NextRetryAfter string `json:"next_retry_after"`
		Quota          struct {
			Exceeded bool `json:"exceeded"`
		} `json:"quota"`
		QuotaWindows []oauthQuotaWindow `json:"quota_windows"`
		PromptUsage  *oauthPromptUsage  `json:"prompt_usage"`
	} `json:"files"`
}

func (g *Gateway) serveOAuthStart(w http.ResponseWriter, r *http.Request) {
	var input oauthStartRequest
	if decodeAdminJSON(w, r, &input) != nil {
		return
	}
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	provider, ok := oauthProviders[input.Provider]
	if !ok {
		writeAPIErrorCode(w, http.StatusBadRequest, "unsupported OAuth provider", "invalid_request_error", "unsupported_oauth_provider")
		return
	}
	query := url.Values{}
	if input.Provider == "gemini" && strings.TrimSpace(input.ProjectID) != "" {
		query.Set("project_id", strings.TrimSpace(input.ProjectID))
	}
	var result oauthAdapterResponse
	if err := callOAuthAdapter(r.Context(), http.MethodGet, provider.Endpoint, query, nil, &result); err != nil {
		writeOAuthAdapterError(w, err)
		return
	}
	if result.URL == "" || !validAuthorizationURL(result.URL) || !validOAuthState(result.State) {
		writeAPIErrorCode(w, http.StatusBadGateway, "OAuth adapter returned an invalid authorization session", "upstream_error", "invalid_oauth_session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": result.Status, "url": result.URL, "state": result.State,
		"provider": input.Provider, "callback_required": provider.CallbackRequired,
	})
}

func (g *Gateway) serveOAuthCallback(w http.ResponseWriter, r *http.Request) {
	var input oauthCallbackInput
	if decodeAdminJSON(w, r, &input) != nil {
		return
	}
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	provider, ok := oauthProviders[input.Provider]
	if !ok || !provider.CallbackRequired {
		writeAPIErrorCode(w, http.StatusBadRequest, "unsupported OAuth callback provider", "invalid_request_error", "unsupported_oauth_provider")
		return
	}
	input.State = strings.TrimSpace(input.State)
	input.RedirectURL = strings.TrimSpace(input.RedirectURL)
	if !validOAuthState(input.State) || !validCallbackURL(input.RedirectURL) {
		writeAPIErrorCode(w, http.StatusBadRequest, "invalid OAuth callback URL or state", "invalid_request_error", "invalid_oauth_callback")
		return
	}
	payload := map[string]string{"provider": input.Provider, "redirect_url": input.RedirectURL, "state": input.State}
	var result oauthAdapterResponse
	if err := callOAuthAdapter(r.Context(), http.MethodPost, "oauth-callback", nil, payload, &result); err != nil {
		writeOAuthAdapterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": result.Status})
}

func (g *Gateway) serveOAuthStatus(w http.ResponseWriter, r *http.Request) {
	var input oauthStatusInput
	if decodeAdminJSON(w, r, &input) != nil {
		return
	}
	input.State = strings.TrimSpace(input.State)
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	if !validOAuthState(input.State) {
		writeAPIErrorCode(w, http.StatusBadRequest, "invalid OAuth state", "invalid_request_error", "invalid_oauth_state")
		return
	}
	var result oauthAdapterResponse
	if err := callOAuthAdapter(r.Context(), http.MethodGet, "get-auth-status", url.Values{"state": []string{input.State}}, nil, &result); err != nil {
		writeOAuthAdapterError(w, err)
		return
	}
	response := map[string]any{"status": result.Status}
	if result.Error != "" {
		response["error"] = result.Error
	}
	if result.Status == "ok" {
		ready, err := g.ensureOAuthPoolAccount(r.Context(), input.Provider)
		response["pool_ready"] = ready
		if err != nil {
			response["warning"] = err.Error()
		}
		if credentials, listErr := listOAuthCredentials(r.Context()); listErr == nil {
			count := 0
			for _, credential := range credentials {
				if input.Provider == "" || credential.Provider == input.Provider {
					count++
				}
			}
			response["credential_count"] = count
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (g *Gateway) serveOAuthAccounts(w http.ResponseWriter, r *http.Request) {
	credentials, err := listOAuthCredentials(r.Context())
	if err != nil {
		writeOAuthAdapterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": credentials})
}

func (g *Gateway) serveOAuthAccountDelete(w http.ResponseWriter, r *http.Request) {
	var input oauthAccountDeleteInput
	if decodeAdminJSON(w, r, &input) != nil {
		return
	}
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		writeAPIErrorCode(w, http.StatusBadRequest, "OAuth account id is required", "invalid_request_error", "invalid_oauth_account")
		return
	}

	name := resolveOAuthAccountName(r.Context(), input.ID)
	if err := callOAuthAdapter(r.Context(), http.MethodDelete, "auth-files", url.Values{"name": []string{name}}, nil, nil); err != nil {
		writeOAuthAdapterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (g *Gateway) serveOAuthAccountStatus(w http.ResponseWriter, r *http.Request) {
	var input oauthAccountStatusInput
	if decodeAdminJSON(w, r, &input) != nil {
		return
	}
	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		writeAPIErrorCode(w, http.StatusBadRequest, "OAuth account id is required", "invalid_request_error", "invalid_oauth_account")
		return
	}
	if input.Disabled == nil {
		writeAPIErrorCode(w, http.StatusBadRequest, "disabled is required", "invalid_request_error", "invalid_oauth_account")
		return
	}

	var result struct {
		Status   string `json:"status"`
		Disabled bool   `json:"disabled"`
	}
	payload := map[string]any{"name": resolveOAuthAccountName(r.Context(), input.ID), "disabled": *input.Disabled}
	if err := callOAuthAdapter(r.Context(), http.MethodPatch, "auth-files/status", nil, payload, &result); err != nil {
		writeOAuthAdapterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "disabled": *input.Disabled})
}

// resolveOAuthAccountName translates the stable auth_index exposed in the
// admin UI to the auth file name accepted by CLIProxyAPI's status endpoint.
// Falling back to the supplied ID keeps this compatible with adapters that
// already accept auth_index directly.
func resolveOAuthAccountName(ctx context.Context, id string) string {
	var payload oauthAuthFilesResponse
	if err := callOAuthAdapter(ctx, http.MethodGet, "auth-files", nil, nil, &payload); err != nil {
		return id
	}
	for _, item := range payload.Files {
		if id != strings.TrimSpace(item.AuthIndex) && id != strings.TrimSpace(item.ID) && id != strings.TrimSpace(item.Name) {
			continue
		}
		if name := firstNonEmpty(strings.TrimSpace(item.Name), strings.TrimSpace(item.ID)); name != "" {
			return name
		}
	}
	return id
}

func listOAuthCredentials(ctx context.Context) ([]oauthCredential, error) {
	var payload oauthAuthFilesResponse
	if err := callOAuthAdapter(ctx, http.MethodGet, "auth-files", nil, nil, &payload); err != nil {
		return nil, err
	}
	credentials := make([]oauthCredential, 0, len(payload.Files))
	for _, item := range payload.Files {
		provider := normalizeOAuthProvider(item.Provider)
		if provider == "" {
			continue
		}
		id := strings.TrimSpace(item.AuthIndex)
		if id == "" {
			id = shortCredentialID(item.ID)
		}
		status := strings.ToLower(strings.TrimSpace(item.Status))
		if item.Disabled {
			status = "disabled"
		} else if item.Unavailable {
			status = "unavailable"
		} else if status == "" {
			status = "active"
		}
		identity := firstNonEmpty(item.Label, item.Email, item.Account, item.ID)
		quotaWindows := make([]oauthQuotaWindow, len(item.QuotaWindows))
		var promptUsage *oauthPromptUsage
		if item.PromptUsage != nil {
			copyPromptUsage := *item.PromptUsage
			promptUsage = &copyPromptUsage
		}
		copy(quotaWindows, item.QuotaWindows)
		credentials = append(credentials, oauthCredential{
			ID: id, Provider: provider, Identity: maskCredentialIdentity(identity),
			Plan: strings.TrimSpace(item.AccountType), Status: status, Disabled: item.Disabled,
			Ready:   status == "active" && !item.Disabled && !item.Unavailable,
			Success: item.Success, Failed: item.Failed, UpdatedAt: item.UpdatedAt, LastRefresh: item.LastRefresh,
			NextRetryAfter: item.NextRetryAfter, QuotaExceeded: item.Quota.Exceeded,
			QuotaWindows: quotaWindows, PromptUsage: promptUsage,
		})
	}
	return credentials, nil
}

// uploadOAuthAuthFile pushes a raw credential bundle to the CLIProxy pool over
// the loopback management channel. CLIProxy stores it as name (which must end in
// .json); a bundle for an existing file name overwrites it, so uploads are
// idempotent. The bundle is JSON-encoded as the request body and CLIProxy takes
// the non-multipart branch of POST /v0/management/auth-files.
func uploadOAuthAuthFile(ctx context.Context, name string, bundle any) error {
	var ack struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := callOAuthAdapter(ctx, http.MethodPost, "auth-files", url.Values{"name": []string{name}}, bundle, &ack); err != nil {
		return err
	}
	if ack.Error != "" {
		return fmt.Errorf("OAuth adapter rejected auth-file %q: %s", name, ack.Error)
	}
	return nil
}

func normalizeOAuthProvider(value string) string {
	switch value = strings.ToLower(strings.TrimSpace(value)); value {
	case "claude":
		return "anthropic"
	case "gemini-cli":
		return "gemini"
	case "openai":
		return "codex"
	default:
		return value
	}
}

func maskCredentialIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "已保存凭据"
	}
	if at := strings.LastIndex(value, "@"); at > 0 && at < len(value)-1 {
		local, domain := []rune(value[:at]), value[at+1:]
		prefix := string(local[:1])
		if len(local) > 2 {
			prefix += string(local[1:2])
		}
		return prefix + "***@" + domain
	}
	runes := []rune(value)
	if len(runes) <= 4 {
		return "***"
	}
	return string(runes[:2]) + "***" + string(runes[len(runes)-2:])
}

func shortCredentialID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 16 {
		return value
	}
	return value[:16]
}

func callOAuthAdapter(ctx context.Context, method, endpoint string, query url.Values, body any, target any) error {
	base, err := oauthAdapterBaseURL()
	if err != nil {
		return err
	}
	key := strings.TrimSpace(os.Getenv("CLIPROXYAPI_MANAGEMENT_KEY"))
	if key == "" {
		return errOAuthAdapterUnavailable
	}
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + "/v0/management/" + endpoint
	u.RawQuery = query.Encode()
	var reader io.Reader
	if body != nil {
		data, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			return errMarshal
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := oauthHTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("OAuth adapter request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read OAuth adapter response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &problem)
		if problem.Error == "" {
			problem.Error = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("OAuth adapter rejected the request: %s", problem.Error)
	}
	if target == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode OAuth adapter response: %w", err)
	}
	return nil
}

func oauthAdapterBaseURL() (*url.URL, error) {
	raw := strings.TrimSpace(os.Getenv("CLIPROXYAPI_MANAGEMENT_URL"))
	if raw == "" {
		raw = defaultOAuthAdapterURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("OAuth adapter URL must be a loopback HTTP URL")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("OAuth adapter URL must use a loopback IP")
	}
	return u, nil
}

func oauthHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			ResponseHeaderTimeout: 10 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("OAuth adapter redirects are disabled") },
	}
}

func validOAuthState(state string) bool { return oauthStatePattern.MatchString(state) }

func validAuthorizationURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

func validCallbackURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 8192 {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}

func writeOAuthAdapterError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	code := "oauth_adapter_error"
	if errors.Is(err, errOAuthAdapterUnavailable) {
		status = http.StatusServiceUnavailable
		code = "oauth_adapter_unavailable"
	}
	writeAPIErrorCode(w, status, err.Error(), "upstream_error", code)
}

func (g *Gateway) ensureOAuthPoolAccount(ctx context.Context, provider string) (bool, error) {
	provider = normalizeOAuthProvider(provider)
	for _, account := range g.Config().Accounts {
		if strings.TrimRight(account.BaseURL, "/") != defaultOAuthAdapterURL+"/v1" {
			continue
		}
		if !account.Enabled || provider != "codex" {
			return account.Enabled, nil
		}
		models, err := discoverOAuthModels(ctx)
		if err != nil {
			return true, fmt.Errorf("OAuth credential was saved, but Codex model discovery failed: %w", err)
		}
		updated := account
		updated.Models = appendUniqueModels(account.Models, models)
		updated.Capabilities = mergeChannelCapabilities(account.Capabilities, config.InferCodexCapabilities(updated.Models))
		if !sameModels(account.Models, updated.Models) || !sameChannelCapabilities(account.Capabilities, updated.Capabilities) {
			if err := g.UpsertAccount(updated); err != nil {
				return false, fmt.Errorf("OAuth credential was saved, but Lite account capability update failed: %w", err)
			}
		}
		return true, nil
	}
	if strings.TrimSpace(os.Getenv("CLIPROXYAPI_KEY")) == "" {
		return false, errors.New("OAuth credential was saved, but CLIPROXYAPI_KEY is not configured")
	}
	models, err := discoverOAuthModels(ctx)
	if err != nil {
		return false, fmt.Errorf("OAuth credential was saved, but model discovery failed: %w", err)
	}
	account := config.Account{
		ID: "cliproxy-oauth", Name: "CLIProxy OAuth Pool", Type: "openai",
		AdapterID: "cli-proxy-api", InstanceID: "local",
		BaseURL: defaultOAuthAdapterURL + "/v1", APIKeyEnv: "CLIPROXYAPI_KEY",
		AuthHeader: "authorization", AuthScheme: "Bearer", Models: models,
		Capabilities: config.InferCodexCapabilities(models),
		Operations:   []string{config.OperationOpenAIChat, config.OperationOpenAIResponses, config.OperationAnthropic},
		Priority:     50, Weight: 1, Concurrency: 4, Enabled: true,
	}
	if err := g.UpsertAccount(account); err != nil {
		return false, fmt.Errorf("OAuth credential was saved, but Lite account creation failed: %w", err)
	}
	return true, nil
}

func discoverOAuthModels(ctx context.Context) ([]string, error) {
	base, err := oauthAdapterBaseURL()
	if err != nil {
		return nil, err
	}
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(os.Getenv("CLIPROXYAPI_KEY")))
	resp, err := oauthHTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model endpoint returned %d", resp.StatusCode)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(payload.Data))
	seen := make(map[string]struct{}, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	if len(models) == 0 {
		return nil, errors.New("no models returned")
	}
	return models, nil
}

func appendUniqueModels(existing, discovered []string) []string {
	models := append([]string(nil), existing...)
	seen := make(map[string]struct{}, len(models)+len(discovered))
	for _, model := range models {
		seen[model] = struct{}{}
	}
	for _, model := range discovered {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	return models
}

func mergeChannelCapabilities(existing, inferred []config.ChannelCapability) []config.ChannelCapability {
	capabilities := append([]config.ChannelCapability(nil), existing...)
	seen := make(map[string]struct{}, len(capabilities)+len(inferred))
	for _, capability := range capabilities {
		seen[capability.Model] = struct{}{}
	}
	for _, capability := range inferred {
		if capability.Model == "" || capability.UpstreamModel == "" {
			continue
		}
		if _, ok := seen[capability.Model]; ok {
			continue
		}
		capability.ReasoningEfforts = append([]string(nil), capability.ReasoningEfforts...)
		capabilities = append(capabilities, capability)
		seen[capability.Model] = struct{}{}
	}
	return capabilities
}

func sameModels(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameChannelCapabilities(left, right []config.ChannelCapability) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Model != right[index].Model || left[index].UpstreamModel != right[index].UpstreamModel || !sameModels(left[index].ReasoningEfforts, right[index].ReasoningEfforts) {
			return false
		}
	}
	return true
}
