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
		`id="accountDialog"`,
		`id="importDialog"`,
		`id="exportDialog"`,
		`function showView(`,
		`function openAccount(`,
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
	if strings.Contains(page, `$('accounts').innerHTML=d.accounts.map(a=>`+"`<div") {
		t.Error("account table body must render table rows, not div elements")
	}
}
