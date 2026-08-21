package web

import (
	"strings"
	"testing"
)

func TestEmbeddedAdminPageStructure(t *testing.T) {
	page := string(IndexHTML)
	required := []string{
		`dataset.ui = "native-v5"`,
		`dataset.ui = "native-v6"`,
		`dataset.ui = "native-v7"`,
		`window.Lite2APINativeV5`,
		`window.Lite2APINativeV6`,
		`window.Lite2APINativeV7`,
		`window.Lite2APINativeV10`,
		`v7ModelDialog`,
		`v7-model-trigger`,
		`v7-effort-control`,
		`搜索模型、上游或真实模型 ID`,
		`id="view-monitor"`,
		`id="view-routes"`,
		`id="view-accounts"`,
		`id="view-keys"`,
		`id="view-prompt-test"`,
		`id="view-adapters"`,
		`id="v10CallCount"`,
		`id="v10SuccessRate"`,
		`id="v10P95Latency"`,
		`id="v10FailoverCount"`,
		`id="v10QuotaBoard"`,
		`id="v10QualityRows"`,
		`id="v10TestAllChannels"`,
		`id="v10ChannelUsageRows"`,
		`id="requestSearch"`,
		`id="requestChart"`,
		`id="latencyChart"`,
		`id="v5RouteList"`,
		`class="v9-route-studio"`,
		`id="routeRows"`,
		`id="routeSaveBtn"`,
		`id="routeChangeSummary"`,
		`id="oauthAccounts"`,
		`id="oauthChannelRail"`,
		`id="oauthAccountSearch"`,
		`id="oauthAccountStatus"`,
		`id="v5SourceAccounts"`,
		`id="v5SourceConnections"`,
		`class="v10-account-workspace"`,
		`id="selectAllAccounts"`,
		`<tbody id="accounts">`,
		`id="v10ProviderGrid"`,
		`id="v10MethodGrid"`,
		`id="v10OnboardingChecklist"`,
		`id="v10TestAccountBtn"`,
		`id="v10AccountTestResult"`,
		`id="themeMode"`,
		`setThemeMode`,
		`lite2api_theme_mode`,
		`data-theme-resolved`,
		`theme-control`,
		`class="v10-import-body"`,
		`id="resultOAuthImported"`,
		`id="resultOAuthSkipped"`,
		`id="resultOAuthFailed"`,
		`id="v5KeyDialog"`,
		`id="quickCreateKeyBtn"`,
		`data-key-preset="personal"`,
		`data-key-preset="temporary"`,
		`data-key-preset="service"`,
		`id="createdKeyCard"`,
		`id="copyCreatedKeyBtn"`,
		`id="clientSetup"`,
		`id="v6ClientBaseURL"`,
		`id="v6KeyList"`,
		`class="table-wrap v6-key-data" hidden`,
		`id="setupBaseURL"`,
		`id="setupModel"`,
		`id="setupCode"`,
		`id="keyAdvanced"`,
		`id="quickAuthDialog"`,
		`id="accountDialog"`,
		`id="importDialog"`,
		`id="exportDialog"`,
		`id="routeJSONDialog"`,
		`function showView(`,
		`function renderRoutes(`,
		`function renderMonitor(`,
		`function renderOAuthAccounts(`,
		`function createQuickKey(`,
		`function createClientKey(`,
		`function renderClientSetup(`,
		`function openQuickAdd(`,
		`function startOAuth(`,
		`function runImport(`,
		`function runExport(`,
		`function targetOperationalState(`,
		`if(!rows.length)return{label:'未知',tone:'unknown'`,
		`const UI_BUILD='2026.08.20-v12'`,
		`window.Lite2APIAccountStatus`,
		`account-toggle`,
		`account-toggle:not(.account-delete)`,
		`account-delete`,
		`method: 'DELETE'`,
		`/oauth/accounts/status`,
		`.nav{grid-template-columns:repeat(4,1fr)}`,
		`.channel-account:not([open]) .quota-strip .quota-window:nth-child(n+3)`,
		`.key-setting-row`,
		`Native v8`,
		`--v8-caret`,
		`Native v9`,
		`--v9-radius`,
		`.v9-route-studio`,
		`Native v10`,
		`.v10-kpi-strip`,
		`id="v12QuotaUsed"`,
		`id="v12OverviewSummary"`,
		`id="v12InsightList"`,
		`data-overview-metric="calls"`,
		`data-overview-metric="latency"`,
		`aria-keyshortcuts="ArrowLeft ArrowRight Home End Escape"`,
		`data-v12-insight-target`,
		`v12-chart-announcer`,
		`.v10-onboarding-body`,
		`.v10-import-body`,
		`Native v12`,
		`--v12-sidebar-w`,
		`scroll-snap-type:x proximity`,
		`flex:0 0 64px`,
		`window.Lite2APINativeV12Motion`,
		`function monotonePath(`,
		`prefers-reduced-motion: reduce`,
		`const requestedView=location.hash.slice(1)`,
		`data-view="adapters"`,
		`data-view="prompt-test"`,
		`v10TestAccountConnection`,
		`v10TestAllChannels`,
		`const reasoningOrder=['auto','none','minimal','low','medium','high','max','xhigh','ultra']`,
		`ultra:'Ultra'`,
		`来自趋势桶`,
		`进程累计 `,
		`请求明细来自当前保留样本`,
		`有成功记录`,
	}
	for _, value := range required {
		if !strings.Contains(page, value) {
			t.Errorf("embedded admin page is missing %q", value)
		}
	}

	forbidden := []string{
		"Lite2API Quiet Control v3",
		"window.Lite2APIQuietControl",
		"Apple Simple 4.0",
		"window.Lite2APIAppleSimple",
		"UI build 2026.08.16-r11",
		`class="route-chain-explain"`,
		`id="token"`,
		`placeholder="管理员 Token"`,
		`seenBuild===UI_BUILD&&`,
		`account-toggle account-delete`,
	}
	for _, value := range forbidden {
		if strings.Contains(page, value) {
			t.Errorf("obsolete or unsafe UI surface leaked into native v10: %q", value)
		}
	}

	headEnd := strings.Index(page, "</head>")
	if headEnd < 0 || strings.LastIndex(page, "</style>") > headEnd {
		t.Error("all styles must remain inside the document head")
	}
	if strings.Count(page, `<style>`) != 1 || strings.Count(page, `</style>`) != 1 {
		t.Error("native v10 must expose one final style element")
	}
	if strings.Count(page, "</html>") != 1 || !strings.HasSuffix(strings.TrimSpace(page), "</body></html>") {
		t.Error("admin page must have exactly one final document closing tag")
	}
	if strings.Contains(page, `Promise.all([api('/state'),api('/client-keys'),api('/adapters'),oauthRequest])`) {
		t.Error("inactive pages must not poll all secondary resources every five seconds")
	}
	if strings.Contains(page, `models:[],rpm:0,concurrency:0,expires_at:''`) || strings.Contains(page, `不限速率 · 永不过期`) {
		t.Error("quick key creation must not default to unlimited, non-expiring access")
	}
	if strings.Contains(page, `for(const account of compatible){`) {
		t.Error("route intent changes must not silently append every compatible upstream")
	}
}

func TestNativeLayoutIsCompileTimeMarkup(t *testing.T) {
	page := string(IndexHTML)
	master := strings.Index(page, `id="v5RouteList"`)
	detail := strings.Index(page, `id="routeRows"`)
	if master < 0 || detail < 0 || master > detail {
		t.Fatal("route master list must exist before the route detail in final HTML")
	}
	if !strings.Contains(page, `<div id="metrics" class="metric-grid upstream-metrics">`) {
		t.Fatal("account quota metrics must remain visible in the primary account layout")
	}
	if !strings.Contains(page, `<dialog id="v5KeyDialog"`) {
		t.Fatal("key creation must remain progressive disclosure")
	}
	if !strings.Contains(page, `id="v6KeyList"`) || !strings.Contains(page, `id="v6ClientBaseURL"`) {
		t.Fatal("client page must expose the endpoint and settings-like key list")
	}
	if !strings.Contains(page, `id="v10ProviderGrid"`) || !strings.Contains(page, `id="v10TestAccountBtn"`) {
		t.Fatal("account onboarding must expose provider selection and pre-save testing")
	}
}

func TestReplaceRange(t *testing.T) {
	base := []byte("before<start>old<end>after")
	got := string(replaceRange(base, []byte("<start>"), []byte("<end>"), []byte("new")))
	if got != "beforenew\n<end>after" {
		t.Fatalf("unexpected replacement: %q", got)
	}
	unchanged := string(replaceRange(base, []byte("missing"), []byte("<end>"), []byte("new")))
	if unchanged != string(base) {
		t.Fatal("missing anchors must leave the document unchanged")
	}
}

func TestBuildIndexHTMLReplacesStyleAndInjectsBehavior(t *testing.T) {
	base := []byte("<html><head><style>old</style></head><body><main>stable</main></body></html>")
	got := string(buildIndexHTML(base, []byte("new"), []byte("enhance()")))
	if strings.Contains(got, "old") || !strings.Contains(got, "<style>\nnew\n</style>") {
		t.Fatalf("canonical style replacement failed: %s", got)
	}
	if !strings.Contains(got, "<script>\nenhance()\n</script>\n</body>") {
		t.Fatalf("behavior injection failed: %s", got)
	}
}
