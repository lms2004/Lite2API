package web

import (
	"strings"
	"testing"
)

func TestEmbeddedAdminPageStructure(t *testing.T) {
	page := string(IndexHTML)
	required := []string{
		`dataset.ui = "native-v5"`,
		`window.Lite2APINativeV5`,
		`id="view-monitor"`,
		`id="view-routes"`,
		`id="view-accounts"`,
		`id="view-keys"`,
		`id="view-prompt-test"`,
		`id="view-adapters"`,
		`id="healthVerdict"`,
		`id="monitorMetrics"`,
		`id="routeHealthSummary"`,
		`id="incidentFeed"`,
		`id="requestSearch"`,
		`id="requestChart"`,
		`id="latencyChart"`,
		`id="v5RouteList"`,
		`class="route-workspace"`,
		`id="routeRows"`,
		`id="routeSaveBtn"`,
		`id="routeChangeSummary"`,
		`id="oauthAccounts"`,
		`id="oauthChannelRail"`,
		`id="oauthAccountSearch"`,
		`id="oauthAccountStatus"`,
		`id="v5SourceAccounts"`,
		`id="v5SourceConnections"`,
		`id="selectAllAccounts"`,
		`<tbody id="accounts">`,
		`id="v5KeyDialog"`,
		`id="quickCreateKeyBtn"`,
		`data-key-preset="personal"`,
		`data-key-preset="temporary"`,
		`data-key-preset="service"`,
		`id="createdKeyCard"`,
		`id="copyCreatedKeyBtn"`,
		`id="clientSetup"`,
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
		`const UI_BUILD='2026.08.18-v5'`,
		`.nav{grid-template-columns:repeat(4,1fr)}`,
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
	}
	for _, value := range forbidden {
		if strings.Contains(page, value) {
			t.Errorf("obsolete or unsafe UI surface leaked into native v5: %q", value)
		}
	}

	headEnd := strings.Index(page, "</head>")
	if headEnd < 0 || strings.LastIndex(page, "</style>") > headEnd {
		t.Error("all styles must remain inside the document head")
	}
	if strings.Count(page, `<style>`) != 1 || strings.Count(page, `</style>`) != 1 {
		t.Error("native v5 must expose one canonical style element")
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

func TestNativeV5IsCompileTimeMarkupNotRuntimeDashboardMove(t *testing.T) {
	page := string(IndexHTML)
	master := strings.Index(page, `id="v5RouteList"`)
	detail := strings.Index(page, `id="routeRows"`)
	if master < 0 || detail < 0 || master > detail {
		t.Fatal("route master list must exist before the route detail in final HTML")
	}
	if !strings.Contains(page, `<div id="metrics" class="metric-grid upstream-metrics" hidden>`) {
		t.Fatal("upstream dashboard metrics must remain available to business rendering but hidden from the primary layout")
	}
	if !strings.Contains(page, `<dialog id="v5KeyDialog"`) {
		t.Fatal("key creation must be progressive disclosure, not a permanent dashboard block")
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
