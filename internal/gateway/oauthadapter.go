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
	State string `json:"state"`
}

type oauthAdapterResponse struct {
	Status string `json:"status"`
	URL    string `json:"url"`
	State  string `json:"state"`
	Error  string `json:"error"`
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
		ready, err := g.ensureOAuthPoolAccount(r.Context())
		response["pool_ready"] = ready
		if err != nil {
			response["warning"] = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, response)
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

func (g *Gateway) ensureOAuthPoolAccount(ctx context.Context) (bool, error) {
	for _, account := range g.Config().Accounts {
		if strings.TrimRight(account.BaseURL, "/") == defaultOAuthAdapterURL+"/v1" {
			return account.Enabled, nil
		}
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
		BaseURL: defaultOAuthAdapterURL + "/v1", APIKeyEnv: "CLIPROXYAPI_KEY",
		AuthHeader: "authorization", AuthScheme: "Bearer", Models: models,
		Priority: 50, Weight: 1, Concurrency: 4, Enabled: true,
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
