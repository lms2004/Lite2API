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
		`id="view-routes"`,
		`id="view-monitor"`,
		`id="view-adapters"`,
		`id="selectAllAccounts"`,
		`<tbody id="accounts">`,
		`id="quickAuthDialog"`,
		`id="accountDialog"`,
		`id="importDialog"`,
		`id="exportDialog"`,
		`function showView(`,
		`function openQuickAdd(`,
		`function startOAuth(`,
		`function submitOAuthCallback(`,
		`function pollOAuthStatus(`,
		`function openAccount(`,
		`function selectAccountTemplate(`,
		`const accountTemplates=`,
		`name="credential_mode"`,
		`id="accountAdvanced"`,
		`VPN 自动授权`,
		`无需填写 Token、Base URL 或模型`,
		`复制链接`,
		`提交并自动添加`,
		`'/oauth/start'`,
		`'/oauth/callback'`,
		`'/oauth/status'`,
		`function openImport(`,
		`function runImport(`,
		`function runExport(`,
		`function renderAdapters(`,
		`'/accounts/import'`,
		`'/accounts/export'`,
		`'/adapters'`,
		`dry_run:dryRun`,
		`include_proxies:`,
		`更多操作`,
		`数据导入`,
		`数据导出`,
		`a.api_key==='********'?'':`,
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
}
