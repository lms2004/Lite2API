package web

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"strings"
	"testing"
)

func TestCanonicalAdminDocument(t *testing.T) {
	page := string(IndexHTML)
	required := []string{
		`data-ui="canonical-usage-console"`, `rel="icon"`, `id="view-usage"`, `id="view-accounts"`,
		`id="view-routes"`, `id="view-clients"`, `id="quotaBoard"`, `id="usageChart"`,
		`id="qualityRows"`, `id="testAllChannels"`, `id="openChannelChatButton"`, `id="addAccountDialog"`,
		`id="manualAccountDialog"`, `id="connectionTestSteps"`, `id="importDialog"`,
		`id="channelChatDialog"`, `id="channelChatMessages"`, `id="channelChatForm"`,
		`id="onboardingResultDialog"`, `id="routeCreateDialog"`, `id="discardRoutesButton"`,
		`id="routeEditor"`, `id="routeCreateModelSelect"`, `id="routeCreateCustomModelField"`,
		`id="clientConfig"`, `id="clientConfigMode"`, `globalThis.Lite2APIAppCore`,
		`function runQuality(`, `function testManualAccount(`, `function runImport(`,
		`function startOAuth(`, `function saveRoutes(`, `function createKey(`,
		`function finishCredentialOnboarding(`, `function validateAndRenderRoutes(`,
		`function connectionTestRequired(`, `function deleteSelectedRoute(`,
		`function openChannelChat(`, `function sendChannelChat(`, `function channelChatResponse(`,
		`/accounts/test`, `/accounts/import`, `/prompt-test`, `/oauth/start`,
		`data-quick-oauth="codex"`, `测试、保存并建路由`, `创建并启用`,
		`关键连接信息变化后必须重新直测`, `固定命中指定连接，不经过 fallback`, `渠道聊天测试`,
	}
	for _, value := range required {
		if !strings.Contains(page, value) {
			t.Errorf("canonical admin page is missing %q", value)
		}
	}
	forbidden := []string{
		"native-v5", "native-v6", "native-v7", "native-v8", "native-v9", "native-v10", "native-v12",
		"class=\"app-shell native-shell\"", "/*__APP_CSS__*/", "/*__APP_JS__*/", "onclick=\"",
		"MutationObserver", "window.selectAccountTemplate", "window.renderRoutes",
		"class=\"segmented account-tabs\"", "创建路由草稿", `id="routeCreateModels"`, `targetModels${index}`,
	}
	for _, value := range forbidden {
		if strings.Contains(page, value) {
			t.Errorf("obsolete layered UI leaked into canonical application: %q", value)
		}
	}
	if strings.Count(page, "<style>") != 1 || strings.Count(page, "</style>") != 1 {
		t.Fatal("canonical application must contain one style element")
	}
	if strings.Count(page, "<script>") != 1 || strings.Count(page, "</script>") != 1 {
		t.Fatal("canonical application must contain one script element")
	}
	if !strings.HasSuffix(strings.TrimSpace(page), "</html>") {
		t.Fatal("canonical document must end with </html>")
	}
}

func TestCanonicalUITrustAndAccessibility(t *testing.T) {
	page := string(IndexHTML)
	required := []string{
		`data-range="3d"`, `aria-pressed="true"`, `aria-current="page"`,
		`id="oauthServiceState"`, `id="retryOAuthAccounts"`, `function oauthErrorPresentation(`,
		`id="chartA11ySummary"`, `id="chartDataRows"`, `function chartKeyboard(`,
		`class="clean-table responsive-table"`, `aria-label="搜索最近请求"`,
		`function usageEvidence(`, `function loadSnapshot(`, `/trends?range=`,
	}
	for _, value := range required {
		if !strings.Contains(page, value) {
			t.Errorf("canonical UI trust/accessibility contract is missing %q", value)
		}
	}
	forbidden := []string{`data-range="30d"`, `APP.range==='30d'`, `role="tabpanel"`}
	for _, value := range forbidden {
		if strings.Contains(page, value) {
			t.Errorf("canonical UI still contains obsolete or misleading markup %q", value)
		}
	}
}

func TestOfficialModelIconsAreEmbeddedUnchanged(t *testing.T) {
	for _, asset := range officialIconAssets {
		if bytes.Contains(IndexHTML, []byte(asset.token)) {
			t.Fatalf("official icon placeholder leaked into page: %s", asset.token)
		}
		data, err := modelIconFS.ReadFile(asset.path)
		if err != nil {
			t.Fatal(err)
		}
		encoded := base64.StdEncoding.EncodeToString(data)
		if !bytes.Contains(IndexHTML, []byte(encoded)) {
			t.Errorf("official icon was not embedded byte-for-byte: %s", asset.path)
		}
	}
}

func TestBuildAppReplacesOnlyCanonicalSlots(t *testing.T) {
	html := []byte(`<html><head><style>/*__APP_CSS__*/</style></head><body><script>/*__APP_JS__*/</script></body></html>`)
	result := buildApp(html, []byte("body{color:black}"), []byte("core()\nboot()"))
	for _, required := range [][]byte{[]byte("body{color:black}"), []byte("core()"), []byte("boot()")} {
		if !bytes.Contains(result, required) {
			t.Fatalf("canonical slot replacement lost %q", required)
		}
	}
	if bytes.Index(result, []byte("core()")) > bytes.Index(result, []byte("boot()")) {
		t.Fatal("domain core must load before the application controller")
	}
}

func TestBuildAppRejectsAmbiguousSlots(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("ambiguous canonical slots must fail closed")
		}
	}()
	buildApp([]byte(`/*__APP_CSS__*/`), nil, nil)
}

func TestCanonicalScriptCSPHashMatchesDocument(t *testing.T) {
	startMarker := []byte("<script>")
	endMarker := []byte("</script>")
	start := bytes.Index(IndexHTML, startMarker)
	end := bytes.Index(IndexHTML, endMarker)
	if start < 0 || end <= start {
		t.Fatal("canonical script element is missing")
	}
	script := IndexHTML[start+len(startMarker) : end]
	if got := cspHash(script); got != ScriptCSPSource {
		t.Fatalf("script CSP source does not match embedded document: got %s want %s", got, ScriptCSPSource)
	}
}

func TestScriptCSPHashIncludesTemplateWhitespace(t *testing.T) {
	page := buildApp([]byte("<style>/*__APP_CSS__*/</style><script>\n  /*__APP_JS__*/\n</script>"), nil, []byte("boot();"))
	script := scriptContent(page)
	if !bytes.Equal(script, []byte("\n  boot();\n")) {
		t.Fatalf("template whitespace was lost: %q", script)
	}
	if cspHash(script) == cspHash(bytes.TrimSpace(script)) {
		t.Fatal("browser CSP hashes must distinguish template whitespace")
	}
}

func TestCanonicalGzipMatchesHTML(t *testing.T) {
	if len(IndexHTMLGzip) == 0 || len(IndexHTMLGzip) >= len(IndexHTML) {
		t.Fatalf("precompressed admin page size=%d original=%d", len(IndexHTMLGzip), len(IndexHTML))
	}
	reader, err := gzip.NewReader(bytes.NewReader(IndexHTMLGzip))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, IndexHTML) {
		t.Fatal("precompressed canonical page must decompress to IndexHTML")
	}
}
