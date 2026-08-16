package web

import (
	"strings"
	"testing"
)

func TestEmbeddedAdminPageStructure(t *testing.T) {
	page := string(IndexHTML)
	required := []string{
		`id="view-accounts"`,
		`id="view-keys"`,
		`id="quickCreateKeyBtn"`,
		`id="createdKeyCard"`,
		`id="copyCreatedKeyBtn"`,
		`id="clientSetup"`,
		`id="setupBaseURL"`,
		`id="setupModel"`,
		`id="setupCode"`,
		`id="keyAdvanced"`,
		`id="view-routes"`,
		`id="view-monitor"`,
		`id="view-prompt-test"`,
		`id="promptAccount"`,
		`id="promptModel"`,
		`id="promptTranscript"`,
		`id="promptInspector"`,
		`const promptTestCases=`,
		`function sendPromptMessage(`,
		`function decodePromptASCII(`,
		`'/prompt-test'`,
		`Token 增量基线`,
		`上下文边界步进`,
		`ASCII 侧信道`,
		`Qwen / ChatML 逃逸`,
		`id="healthVerdict"`,
		`id="routeHealthSummary"`,
		`id="incidentFeed"`,
		`id="view-adapters"`,
		`id="selectAllAccounts"`,
		`<tbody id="accounts">`,
		`id="quickAuthDialog"`,
		`id="webProxyDialog"`,
		`id="oauthAccounts"`,
		`id="oauthChannelRail"`,
		`id="oauthAccountSearch"`,
		`id="oauthAccountStatus"`,
		`id="accountDialog"`,
		`id="importDialog"`,
		`id="exportDialog"`,
		`function showView(`,
		`function createQuickKey(`,
		`function createClientKey(`,
		`function copyCreatedKey(`,
		`function renderClientSetup(`,
		`function copyClientSetup(`,
		`function gatewayAPIBase(`,
		`ANTHROPIC_BASE_URL`,
		`LITE2API_API_KEY`,
		`model_providers.lite2api`,
		`OPENAI_BASE_URL`,
		`function openQuickAdd(`,
		`function startOAuth(`,
		`function submitOAuthCallback(`,
		`function pollOAuthStatus(`,
		`function renderOAuthAccounts(`,
		`function renderOAuthUsage(`,
		`prompt_usage`,
		`暂无真实 usage`,
		`注入后输入 / 上游 input tokens`,
		`真实 provider usage`,
		`function quotaWindowHTML(`,
		`role="meter"`,
		`quota_windows`,
		`function refreshDelay(`,
		`activeViewName==='accounts'`,
		`function openWebProxyGuide(`,
		`function normalizeWebCredential(`,
		`function openAccount(`,
		`function selectAccountTemplate(`,
		`const accountTemplates=`,
		`name="credential_mode"`,
		`id="accountAdvanced"`,
		`授权凭据由本机隔离适配器保存`,
		`OAuth/设备授权直接生成链接`,
		`复制链接`,
		`提交并加入认证池`,
		`'/oauth/start'`,
		`'/oauth/callback'`,
		`'/oauth/status'`,
		`'/oauth/accounts'`,
		`function openImport(`,
		`function runImport(`,
		`function runExport(`,
		`function renderAdapters(`,
		`'/accounts/import'`,
		`'/accounts/export'`,
		`'/adapters'`,
		`dry_run:dryRun`,
		`include_proxies:`,
		`快速创建`,
		`立即生成并复制`,
		`仅显示一次`,
		`更多操作`,
		`数据导入`,
		`数据导出`,
		`a.api_key==='********'?'':`,
		`id="b-openai"`,
		`id="b-anthropic"`,
		`id="b-gemini"`,
		`id="b-deepseek"`,
		`id="b-x"`,
		`function providerKey(`,
		`function providerMark(`,
		`provider-icon provider-`,
		`id="routeRows"`,
		`id="routeSaveBtn"`,
		`id="routeJSONDialog"`,
		`id="monitorMetrics"`,
		`id="requestChart"`,
		`id="requestSearch"`,
		`id="adapterChips"`,
		`function renderRoutes(`,
		`function updateTarget(`,
		`function addTarget(`,
		`function moveTarget(`,
		`function channelCapabilities(`,
		`function compatibleChannelAccounts(`,
		`function updateRouteIntent(`,
		`reasoning_effort`,
		`渠道是实际凭据或接入来源`,
		`function renderMonitor(`,
		`function renderRouteHealth(`,
		`function renderIncidents(`,
		`function percentile(`,
		`function drawRequestChart(`,
		`function setAdapterStatus(`,
	}
	for _, value := range required {
		if !strings.Contains(page, value) {
			t.Errorf("embedded admin page is missing %q", value)
		}
	}

	headEnd := strings.Index(page, "</head>")
	if headEnd < 0 || strings.LastIndex(page, "</style>") > headEnd {
		t.Error("all style elements must be inside the document head")
	}
	if strings.Count(page, "</html>") != 1 || !strings.HasSuffix(strings.TrimSpace(page), "</body></html>") {
		t.Error("admin page must have exactly one final document closing tag")
	}
	if strings.Contains(page, `id="token"`) || strings.Contains(page, `placeholder="管理员 Token"`) {
		t.Error("VPN-only admin page must not ask the user to enter an admin token")
	}
	if strings.Contains(page, `$('accounts').innerHTML=d.accounts.map(a=>`+"`<div") {
		t.Error("account table body must render table rows, not div elements")
	}
	if strings.Contains(page, `>×</button>`) || strings.Contains(page, `<div class="drop-icon">⇧</div>`) {
		t.Error("dashboard controls must use the shared icon system instead of character glyphs")
	}
	if !strings.Contains(page, `id="view-monitor" class="view active"`) || strings.Contains(page, `id="view-accounts" class="view active"`) {
		t.Error("operations overview must be the default view")
	}
	if !strings.Contains(page, `id="routeSaveBtn" class="primary" onclick="saveRoutes()" disabled`) {
		t.Error("route save action must stay hidden until there are unsaved changes")
	}
	if strings.Contains(page, `Promise.all([api('/state'),api('/client-keys'),api('/adapters'),oauthRequest])`) {
		t.Error("inactive pages must not poll keys, adapters and OAuth accounts every five seconds")
	}
}
