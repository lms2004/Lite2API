package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

type oauthRefreshEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AuthIndex string `json:"auth_index"`
	Provider  string `json:"provider"`
	Error     string `json:"error"`
}

type oauthRefreshResult struct {
	Status    string              `json:"status"`
	Refreshed int                 `json:"refreshed"`
	Skipped   int                 `json:"skipped"`
	Files     []oauthRefreshEntry `json:"files"`
	Failed    []oauthRefreshEntry `json:"failed"`
}

// Raw adapter errors and filenames can contain credentials or personal data.
// Only stable public IDs and locally authored diagnostic strings leave here.
func sanitizedOAuthRefreshEntries(entries []oauthRefreshEntry, failed bool) []map[string]string {
	result := make([]map[string]string, 0, len(entries))
	for _, entry := range entries {
		id := entry.AuthIndex
		if id == "" {
			id = shortCredentialID(firstNonEmpty(entry.ID, entry.Name))
		}
		item := map[string]string{"id": id, "provider": normalizeOAuthProvider(entry.Provider)}
		if failed {
			code, message := oauthHealthReason("error", entry.Error, "", false)
			item["code"], item["error"] = code, message
		}
		result = append(result, item)
	}
	return result
}

func oauthHealthReason(status, message, retryAfter string, quotaExceeded bool) (string, string) {
	text := strings.ToLower(message)
	switch {
	case status == "disabled":
		return "disabled", "账号已停用"
	case strings.Contains(text, "reauth_required"), strings.Contains(text, "invalid_grant"),
		strings.Contains(text, "refresh_token_reused"), strings.Contains(text, "re-authenticate"),
		strings.Contains(text, "signing in again"), strings.Contains(text, "token expired"),
		strings.Contains(text, "token has expired"), strings.Contains(text, "token revoked"):
		return "reauth_required", "授权已失效，请重新登录后恢复调用"
	case quotaExceeded:
		return "quota_exceeded", "额度已耗尽，等待重置或切换账号"
	case status == "unavailable" && retryAfter != "":
		if retry, err := time.Parse(time.RFC3339Nano, retryAfter); err == nil && retry.After(time.Now()) {
			return "cooldown", "账号暂时冷却，等待恢复或切换账号"
		}
	}
	if status != "active" {
		return "upstream_error", "账号最近出现异常，请刷新授权或检查连接"
	}
	return "", ""
}

type oauthRecoveryCall struct {
	done    chan struct{}
	expires time.Time
	ok      bool
}

type oauthRecoveryCoordinator struct {
	mu    sync.Mutex
	calls map[[sha256.Size]byte]*oauthRecoveryCall
}

// Refresh is bounded and coalesced per credential. Entries include the
// management identity so a changed endpoint/key cannot reuse old evidence.
func (c *oauthRecoveryCoordinator) refresh(ctx context.Context, credential string, refresh func(context.Context) bool) bool {
	key := sha256.Sum256([]byte(os.Getenv("CLIPROXYAPI_MANAGEMENT_URL") + "\x00" + os.Getenv("CLIPROXYAPI_MANAGEMENT_KEY") + "\x00" + credential))
	c.mu.Lock()
	if c.calls == nil {
		c.calls = make(map[[sha256.Size]byte]*oauthRecoveryCall)
	}
	now := time.Now()
	for id, call := range c.calls {
		if !call.expires.IsZero() && now.After(call.expires) {
			delete(c.calls, id)
		}
	}
	if call := c.calls[key]; call != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-call.done:
			return call.ok && ctx.Err() == nil
		}
	}
	if len(c.calls) >= 128 || ctx.Err() != nil {
		c.mu.Unlock()
		return false
	}
	call := &oauthRecoveryCall{done: make(chan struct{})}
	c.calls[key] = call
	c.mu.Unlock()
	refreshCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	ok := refresh(refreshCtx)
	cancel()
	c.mu.Lock()
	call.ok = ok
	call.expires = time.Now().Add(time.Minute)
	if ctx.Err() != nil {
		delete(c.calls, key)
	}
	close(call.done)
	c.mu.Unlock()
	return ok && ctx.Err() == nil
}

func trustedOAuthAccount(account config.Account) bool {
	if !strings.EqualFold(strings.TrimSpace(account.AdapterID), "cli-proxy-api") || account.ProxyURL != "" {
		return false
	}
	base, err := oauthAdapterBaseURL()
	upstream, parseErr := url.Parse(account.BaseURL)
	return err == nil && parseErr == nil && upstream.User == nil && upstream.Scheme == base.Scheme && upstream.Host == base.Host
}

func refreshOAuthCredential(ctx context.Context, credential string) bool {
	if credential == "" || strings.TrimSpace(os.Getenv("CLIPROXYAPI_MANAGEMENT_KEY")) == "" {
		return false
	}
	var pool oauthAuthFilesResponse
	if callOAuthAdapter(ctx, http.MethodGet, "auth-files", nil, nil, &pool) != nil {
		return false
	}
	for _, item := range pool.Files {
		if item.AuthIndex != credential || item.Disabled || item.RuntimeOnly {
			continue
		}
		name := firstNonEmpty(item.Name, item.ID)
		if name == "" {
			return false
		}
		var result oauthRefreshResult
		return callOAuthAdapter(ctx, http.MethodPost, "auth-files/refresh", nil, map[string]string{"name": name}, &result) == nil && result.Refreshed == 1 && len(result.Failed) == 0
	}
	return false
}

// Only an explicit 401 proves that the provider rejected the submission. No
// timeout, transport error, 5xx or partially delivered stream is replayed here.
func (g *Gateway) doUpstreamWithOAuthRecovery(ctx context.Context, state *runtimeState, selection *Selection, inbound *http.Request, body []byte, id string) (*http.Response, error) {
	resp, err := g.doUpstream(ctx, state, selection.Account, inbound, body, id, selection.Credential)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || !trustedOAuthAccount(selection.Account.Config) {
		return resp, err
	}
	credential := firstNonEmpty(strings.TrimSpace(resp.Header.Get(credentialSelectedHeader)), selection.Credential)
	if credential == "" || strings.TrimSpace(os.Getenv("CLIPROXYAPI_MANAGEMENT_KEY")) == "" {
		return resp, nil
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(strings.NewReader(""))
	if !g.oauthRecovery.refresh(ctx, credential, func(refreshCtx context.Context) bool {
		return refreshOAuthCredential(refreshCtx, credential)
	}) {
		return resp, nil
	}
	return g.doUpstream(ctx, state, selection.Account, inbound, body, id, selection.Credential)
}

func normalizeUpstreamAuthError(response *bufferedResponse, operation string) {
	if response.status != http.StatusUnauthorized && response.status != http.StatusForbidden {
		return
	}
	const message = "upstream account authorization failed; refresh or reconnect the upstream account"
	var payload any = map[string]any{"error": map[string]any{"message": message, "type": "upstream_error", "code": "upstream_auth_failed", "param": nil}}
	if operation == config.OperationAnthropic {
		payload = map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": message}}
	}
	response.status = http.StatusBadGateway
	response.header = http.Header{"Content-Type": {"application/json; charset=utf-8"}}
	response.body, _ = json.Marshal(payload)
	response.readError = ""
}

func normalizeOAuthUnavailableError(response *bufferedResponse, operation string) {
	const message = "no usable upstream account is available; check account authorization and cooldowns"
	var payload any = map[string]any{"error": map[string]any{"message": message, "type": "upstream_error", "code": "upstream_unavailable", "param": nil}}
	if operation == config.OperationAnthropic {
		payload = map[string]any{"type": "error", "error": map[string]string{"type": "overloaded_error", "message": message}}
	}
	response.header = http.Header{"Content-Type": {"application/json; charset=utf-8"}}
	response.body, _ = json.Marshal(payload)
	response.readError = ""
}
