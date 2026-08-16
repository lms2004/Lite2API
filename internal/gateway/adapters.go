package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lms2004/lite2api/internal/config"
)

// AdapterDescriptor is a curated, auditable integration record. Catalog entries
// are capabilities, not an instruction to deploy unreviewed third-party code.
type AdapterDescriptor struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Category      string   `json:"category"`
	Platforms     []string `json:"platforms"`
	Protocols     []string `json:"protocols"`
	Operations    []string `json:"operations"`
	AuthModes     []string `json:"auth_modes"`
	Status        string   `json:"status"`
	InstallStatus string   `json:"install_status"`
	RuntimeStatus string   `json:"runtime_status"`
	Readiness     string   `json:"readiness"`
	Traffic       string   `json:"traffic"`
	CheckedAt     string   `json:"checked_at,omitempty"`
	LatencyMS     int64    `json:"latency_ms,omitempty"`
	ModelCount    int      `json:"model_count,omitempty"`
	Maturity      string   `json:"maturity"`
	License       string   `json:"license"`
	SourceURL     string   `json:"source_url,omitempty"`
	LocalURL      string   `json:"local_url,omitempty"`
	Migration     []string `json:"migration"`
	AccountIDs    []string `json:"account_ids,omitempty"`
	Description   string   `json:"description"`
}

var adapterCatalog = []AdapterDescriptor{
	{ID: "generic-openai", Name: "OpenAI 兼容接口", Category: "native", Platforms: []string{"OpenAI", "DeepSeek", "Mistral", "Moonshot", "Groq", "Together", "Fireworks", "任意兼容服务"}, Protocols: []string{"openai-chat", "openai-responses", "openai-embeddings", "openai-images", "openai-rerank"}, AuthModes: []string{"api-key", "none"}, Status: "built-in", Maturity: "stable", License: "n/a", Migration: []string{"api-key"}, Description: "Lite2API 原生接入任意 OpenAI 兼容 Base URL。"},
	{ID: "generic-anthropic", Name: "Anthropic 原生接口", Category: "native", Platforms: []string{"Anthropic", "Claude"}, Protocols: []string{"anthropic-messages"}, AuthModes: []string{"api-key"}, Status: "built-in", Maturity: "stable", License: "n/a", Migration: []string{"api-key"}, Description: "Lite2API 原生转发 Anthropic Messages。"},
	{ID: "gemini-openai", Name: "Gemini OpenAI 兼容端点", Category: "native", Platforms: []string{"Gemini"}, Protocols: []string{"openai-chat", "openai-embeddings"}, AuthModes: []string{"api-key"}, Status: "built-in", Maturity: "stable", License: "n/a", Migration: []string{"api-key"}, Description: "Google 官方 OpenAI 兼容端点，可直接使用 Gemini API Key。"},
	{ID: "atomcode2api", Name: "AtomCode2API", Category: "specialized", Platforms: []string{"AtomCode", "DeepSeek", "Qwen"}, Protocols: []string{"openai-chat"}, AuthModes: []string{"cookie", "token"}, Status: "installed", Maturity: "stable", License: "external", LocalURL: "http://127.0.0.1:45678/v1", Migration: []string{"manual"}, Description: "现有隔离进程，负责 AtomCode 会话协议。"},
	{ID: "grok2api", Name: "Grok2API", Category: "specialized", Platforms: []string{"Grok", "xAI"}, Protocols: []string{"openai-chat"}, AuthModes: []string{"oauth", "cookie", "token"}, Status: "installed", Maturity: "stable", License: "AGPL-3.0", SourceURL: "https://github.com/chenyme/grok2api", LocalURL: "http://127.0.0.1:45680/v1", Migration: []string{"adapter-import", "manual"}, Description: "Grok Build/Web/Console 多账号适配器。"},
	{ID: "gemini-web2api", Name: "Gemini Web2API", Category: "specialized", Platforms: []string{"Gemini Web"}, Protocols: []string{"openai-chat"}, AuthModes: []string{"cookie", "anonymous"}, Status: "installed", Maturity: "stable", License: "MIT", SourceURL: "https://github.com/HanaokaYuzu/Gemini-Web2API", LocalURL: "http://127.0.0.1:45681/v1", Migration: []string{"manual"}, Description: "Gemini Web 会话适配器，Cookie 与核心隔离。"},
	{ID: "cli-proxy-api", Name: "CLIProxyAPI", Category: "oauth-hub", Platforms: []string{"OpenAI/Codex", "Claude", "Gemini CLI", "Antigravity"}, Protocols: []string{"openai-chat", "openai-responses", "anthropic-messages", "gemini"}, AuthModes: []string{"oauth", "setup-token", "api-key"}, Status: "installed", Maturity: "pinned", License: "MIT", SourceURL: "https://github.com/router-for-me/CLIProxyAPI", LocalURL: "http://127.0.0.1:45682/v1", Migration: []string{"sub2api-oauth", "auth-files"}, Description: "固定 v6.10.9 的 OAuth 聚合适配器；账号文件与 Lite2API 核心隔离。"},
	{ID: "auth2api", Name: "auth2api", Category: "oauth-hub", Platforms: []string{"Claude", "Codex", "Cursor"}, Protocols: []string{"openai-chat", "anthropic-messages"}, AuthModes: []string{"oauth"}, Status: "catalog", Maturity: "review", License: "verify", SourceURL: "https://github.com/AmazingAng/auth2api", Migration: []string{"manual"}, Description: "多 OAuth 提供商候选，尚未纳入生产编排。"},
	{ID: "antigravity-proxy", Name: "Antigravity Proxy", Category: "specialized", Platforms: []string{"Antigravity"}, Protocols: []string{"openai-chat"}, AuthModes: []string{"oauth"}, Status: "catalog", Maturity: "review", License: "verify", SourceURL: "https://github.com/frieser/antigravity-proxy", Migration: []string{"manual"}, Description: "Antigravity 专用候选适配器。"},
	{ID: "anti-api", Name: "anti-api", Category: "oauth-hub", Platforms: []string{"Antigravity", "Codex", "GitHub Copilot", "Kiro"}, Protocols: []string{"openai-chat"}, AuthModes: []string{"oauth"}, Status: "catalog", Maturity: "review", License: "verify", SourceURL: "https://github.com/ink1ing/anti-api", Migration: []string{"manual"}, Description: "桌面编码订阅聚合候选。"},
	{ID: "codex-proxy", Name: "codex-proxy", Category: "specialized", Platforms: []string{"OpenAI/Codex"}, Protocols: []string{"openai-responses", "openai-chat"}, AuthModes: []string{"oauth"}, Status: "catalog", Maturity: "review", License: "verify", SourceURL: "https://github.com/wowyuarm/codex-proxy", Migration: []string{"manual"}, Description: "Codex OAuth 专用候选。"},
	{ID: "openai-oauth", Name: "openai-oauth", Category: "specialized", Platforms: []string{"OpenAI"}, Protocols: []string{"openai-chat"}, AuthModes: []string{"oauth"}, Status: "catalog", Maturity: "review", License: "verify", SourceURL: "https://github.com/EvanZhouDev/openai-oauth", Migration: []string{"manual"}, Description: "OpenAI OAuth 候选适配器。"},
	{ID: "codex-gateway", Name: "codex-gateway", Category: "specialized", Platforms: []string{"OpenAI/Codex"}, Protocols: []string{"openai-responses", "openai-chat"}, AuthModes: []string{"oauth"}, Status: "catalog", Maturity: "review", License: "verify", SourceURL: "https://github.com/LanternCX/codex-gateway", Migration: []string{"manual"}, Description: "Codex 网关候选。"},
	{ID: "copilot-api", Name: "copilot-api", Category: "specialized", Platforms: []string{"GitHub Copilot"}, Protocols: []string{"openai-chat"}, AuthModes: []string{"oauth", "token"}, Status: "catalog", Maturity: "review", License: "verify", SourceURL: "https://github.com/caozhiyuan/copilot-api", Migration: []string{"manual"}, Description: "GitHub Copilot 候选适配器。"},
	{ID: "code-proxy", Name: "code-proxy", Category: "oauth-hub", Platforms: []string{"Coding assistants"}, Protocols: []string{"openai-chat"}, AuthModes: []string{"oauth", "token"}, Status: "catalog", Maturity: "review", License: "verify", SourceURL: "https://github.com/rodrigorodriguescosta/code-proxy", Migration: []string{"manual"}, Description: "编码助手聚合候选。"},
	{ID: "gemini-proxy", Name: "gemini-proxy", Category: "specialized", Platforms: []string{"Gemini"}, Protocols: []string{"openai-chat", "gemini"}, AuthModes: []string{"oauth", "api-key"}, Status: "catalog", Maturity: "review", License: "verify", SourceURL: "https://github.com/KashifKhn/gemini-proxy", Migration: []string{"manual"}, Description: "Gemini 专用候选适配器。"},
	{ID: "codex-pool", Name: "codex-pool", Category: "pool", Platforms: []string{"OpenAI/Codex"}, Protocols: []string{"openai-responses"}, AuthModes: []string{"oauth"}, Status: "catalog", Maturity: "review", License: "verify", SourceURL: "https://github.com/darvell/codex-pool", Migration: []string{"manual"}, Description: "Codex 多账号池候选。"},
	{ID: "litellm", Name: "LiteLLM Proxy", Category: "aggregator", Platforms: []string{"100+ providers"}, Protocols: []string{"openai-chat", "openai-responses"}, AuthModes: []string{"api-key", "cloud-credentials"}, Status: "catalog", Maturity: "stable", License: "MIT", SourceURL: "https://github.com/BerriAI/litellm", Migration: []string{"openai-compatible"}, Description: "广覆盖上游聚合器，可作为 Lite2API 的单一隔离上游。"},
	{ID: "ollama", Name: "Ollama", Category: "local-inference", Platforms: []string{"Local models"}, Protocols: []string{"openai-chat", "ollama"}, AuthModes: []string{"none"}, Status: "catalog", Maturity: "stable", License: "MIT", SourceURL: "https://github.com/ollama/ollama", Migration: []string{"openai-compatible"}, Description: "本地模型运行时。"},
	{ID: "vllm", Name: "vLLM", Category: "local-inference", Platforms: []string{"Local models"}, Protocols: []string{"openai-chat", "openai-responses"}, AuthModes: []string{"none", "api-key"}, Status: "catalog", Maturity: "stable", License: "Apache-2.0", SourceURL: "https://github.com/vllm-project/vllm", Migration: []string{"openai-compatible"}, Description: "高吞吐 OpenAI 兼容推理服务。"},
	{ID: "sglang", Name: "SGLang", Category: "local-inference", Platforms: []string{"Local models"}, Protocols: []string{"openai-chat"}, AuthModes: []string{"none", "api-key"}, Status: "catalog", Maturity: "stable", License: "Apache-2.0", SourceURL: "https://github.com/sgl-project/sglang", Migration: []string{"openai-compatible"}, Description: "高性能本地推理服务。"},
	{ID: "llama-cpp", Name: "llama.cpp server", Category: "local-inference", Platforms: []string{"GGUF local models"}, Protocols: []string{"openai-chat"}, AuthModes: []string{"none"}, Status: "catalog", Maturity: "stable", License: "MIT", SourceURL: "https://github.com/ggml-org/llama.cpp", Migration: []string{"openai-compatible"}, Description: "轻量本地 GGUF 推理服务。"},
	{ID: "localai", Name: "LocalAI", Category: "local-inference", Platforms: []string{"Local models"}, Protocols: []string{"openai-chat", "openai-images", "openai-audio"}, AuthModes: []string{"none", "api-key"}, Status: "catalog", Maturity: "stable", License: "MIT", SourceURL: "https://github.com/mudler/LocalAI", Migration: []string{"openai-compatible"}, Description: "多模态本地 OpenAI 兼容服务。"},
}

func AdapterCatalog(accounts []config.Account) []AdapterDescriptor {
	items := make([]AdapterDescriptor, len(adapterCatalog))
	for index := range adapterCatalog {
		item := adapterCatalog[index]
		installStatus := item.Status
		item.Platforms = append([]string(nil), item.Platforms...)
		item.Protocols = append([]string(nil), item.Protocols...)
		item.Operations = operationsForProtocols(item.Protocols)
		item.AuthModes = append([]string(nil), item.AuthModes...)
		item.Migration = append([]string(nil), item.Migration...)
		for _, account := range accounts {
			if adapterMatchesAccount(item, account) {
				item.AccountIDs = append(item.AccountIDs, account.ID)
			}
		}
		if len(item.AccountIDs) > 0 && item.Status != "built-in" {
			item.Status = "configured"
		}
		item.InstallStatus = installStatus
		item.Traffic = "disabled"
		if item.Status == "built-in" {
			item.RuntimeStatus, item.Readiness = "in-process", "ready"
			if len(item.AccountIDs) > 0 {
				item.Traffic = "enabled"
			}
		} else if len(item.AccountIDs) > 0 {
			item.Traffic = "enabled"
		}
		items[index] = item
	}
	return items
}

type adapterProbeResult struct {
	running    bool
	modelCount int
	latency    time.Duration
	checkedAt  time.Time
}

type adapterProbeCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]adapterProbeResult
	client  *http.Client
}

func newAdapterProbeCache(ttl time.Duration) *adapterProbeCache {
	return &adapterProbeCache{
		ttl: ttl, entries: make(map[string]adapterProbeResult),
		client: &http.Client{
			Timeout: 500 * time.Millisecond,
			Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext,
				ResponseHeaderTimeout: 300 * time.Millisecond,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("adapter redirects are disabled") },
		},
	}
}

func (g *Gateway) AdapterCatalog(ctx context.Context, accounts []config.Account) []AdapterDescriptor {
	items := AdapterCatalog(accounts)
	for index := range items {
		item := &items[index]
		if item.LocalURL == "" || (item.InstallStatus != "installed" && item.InstallStatus != "configured") {
			continue
		}
		result := g.adapterProbe.probe(ctx, *item)
		applyAdapterProbe(item, result)
	}
	return items
}

func (c *adapterProbeCache) probe(ctx context.Context, item AdapterDescriptor) adapterProbeResult {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.entries[item.ID]; ok && now.Sub(cached.checkedAt) < c.ttl {
		return cached
	}
	result := c.probeNow(ctx, item, now)
	c.entries[item.ID] = result
	return result
}

func (c *adapterProbeCache) probeNow(ctx context.Context, item AdapterDescriptor, now time.Time) adapterProbeResult {
	result := adapterProbeResult{checkedAt: now}
	base, err := url.Parse(item.LocalURL)
	if err != nil || base.Scheme != "http" || net.ParseIP(base.Hostname()) == nil || !net.ParseIP(base.Hostname()).IsLoopback() {
		return result
	}
	base.Path = strings.TrimSuffix(strings.TrimRight(base.Path, "/"), "/v1") + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return result
	}
	started := time.Now()
	resp, err := c.client.Do(req)
	result.latency = time.Since(started)
	if err != nil {
		return result
	}
	resp.Body.Close()
	result.running = resp.StatusCode >= 200 && resp.StatusCode < 500
	if result.running && item.ID == "cli-proxy-api" {
		result.modelCount = c.cliProxyModels(ctx, item.LocalURL)
	}
	return result
}

func (c *adapterProbeCache) cliProxyModels(ctx context.Context, localURL string) int {
	key := strings.TrimSpace(os.Getenv("CLIPROXYAPI_KEY"))
	if key == "" {
		return 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(localURL, "/")+"/models", nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := c.client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload) != nil {
		return 0
	}
	return len(payload.Data)
}

func applyAdapterProbe(item *AdapterDescriptor, result adapterProbeResult) {
	item.CheckedAt = result.checkedAt.UTC().Format(time.RFC3339)
	item.LatencyMS = result.latency.Milliseconds()
	item.ModelCount = result.modelCount
	if !result.running {
		item.Status, item.RuntimeStatus, item.Readiness, item.Traffic = "stopped", "stopped", "unavailable", "disabled"
		return
	}
	item.RuntimeStatus = "running"
	if item.ID == "cli-proxy-api" && result.modelCount == 0 {
		item.Status, item.Readiness, item.Traffic = "auth-required", "auth-required", "disabled"
		return
	}
	if len(item.AccountIDs) == 0 {
		item.Status, item.Readiness, item.Traffic = "running", "unconfigured", "disabled"
		return
	}
	item.Status, item.Readiness, item.Traffic = "ready", "ready", "enabled"
}

func operationsForProtocols(protocols []string) []string {
	result := make([]string, 0, len(protocols))
	seen := make(map[string]struct{})
	for _, protocol := range protocols {
		var operation string
		switch protocol {
		case "openai-chat":
			operation = config.OperationOpenAIChat
		case "openai-responses":
			operation = config.OperationOpenAIResponses
		case "anthropic-messages":
			operation = config.OperationAnthropic
		case "openai-embeddings":
			operation = config.OperationEmbeddings
		case "openai-images":
			operation = config.OperationImages
		case "openai-rerank":
			operation = config.OperationRerank
		}
		if operation == "" {
			continue
		}
		if _, ok := seen[operation]; ok {
			continue
		}
		seen[operation] = struct{}{}
		result = append(result, operation)
	}
	return result
}

func adapterMatchesAccount(adapter AdapterDescriptor, account config.Account) bool {
	if strings.EqualFold(strings.TrimSpace(account.AdapterID), adapter.ID) {
		return true
	}
	baseURL := strings.TrimRight(strings.ToLower(strings.TrimSpace(account.BaseURL)), "/")
	localURL := strings.TrimRight(strings.ToLower(strings.TrimSpace(adapter.LocalURL)), "/")
	if localURL != "" && baseURL == localURL {
		return true
	}
	hints := strings.ToLower(account.ID + " " + account.Name)
	return adapter.ID != "generic-openai" && adapter.ID != "generic-anthropic" && strings.Contains(hints, adapter.ID)
}

func adapterForImport(hints ...string) string {
	value := strings.ToLower(strings.Join(hints, " "))
	switch {
	case strings.Contains(value, "antigravity"):
		return "cli-proxy-api"
	case strings.Contains(value, "anthropic"), strings.Contains(value, "claude"), strings.Contains(value, "setup-token"):
		return "cli-proxy-api"
	case strings.Contains(value, "openai"), strings.Contains(value, "codex"):
		return "cli-proxy-api"
	case strings.Contains(value, "grok"), strings.Contains(value, "xai"):
		return "grok2api"
	case strings.Contains(value, "gemini"):
		return "cli-proxy-api"
	default:
		return "cli-proxy-api"
	}
}
