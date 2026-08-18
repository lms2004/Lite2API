package web

import (
	"strings"
	"testing"
)

func TestAppleSimpleV4IsEmbedded(t *testing.T) {
	page := string(IndexHTML)
	required := []string{
		"Lite2API Apple Simple v4",
		"window.Lite2APIAppleSimple",
		`dataset.ui = "apple-simple-v4"`,
		`--canvas: #f5f5f7`,
		`--action: #0071e3`,
		`.apple-overview-summary`,
		`.apple-route-workspace`,
		`.apple-route-sidebar`,
		`.apple-route-item`,
		`.apple-key-creator`,
		`lite2api.apple.route`,
		`prefers-color-scheme: dark`,
	}
	for _, marker := range required {
		if !strings.Contains(page, marker) {
			t.Errorf("Apple-simple admin page is missing %q", marker)
		}
	}
}

func TestAppleSimpleV4KeepsBusinessSurface(t *testing.T) {
	page := string(IndexHTML)
	required := []string{
		`id="view-monitor"`,
		`id="view-routes"`,
		`id="view-accounts"`,
		`id="view-keys"`,
		`id="healthVerdict"`,
		`id="monitorMetrics"`,
		`id="routeRows"`,
		`id="quickCreateKeyBtn"`,
		`id="createdKeyCard"`,
		`id="clientSetup"`,
		`function renderRoutes(`,
		`function createQuickKey(`,
		`function openQuickAdd(`,
	}
	for _, marker := range required {
		if !strings.Contains(page, marker) {
			t.Errorf("Apple-simple layout must preserve business contract %q", marker)
		}
	}
}

func TestAppleSimpleV4UsesSplitViewRatherThanCardWall(t *testing.T) {
	css := string(appleLayoutV4CSS)
	checks := []string{
		`grid-template-columns: 270px minmax(0, 1fr)`,
		`.apple-route-workspace > .route-panel`,
		`.apple-overview-summary .monitor-metrics`,
		`.apple-key-create-trigger`,
		`.qc-source-tabs`,
	}
	for _, marker := range checks {
		if !strings.Contains(css, marker) {
			t.Errorf("macro-layout contract is missing %q", marker)
		}
	}
}
