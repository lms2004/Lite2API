package web

import (
	"strings"
	"testing"
)

func TestQuietControlAssetsAreCanonical(t *testing.T) {
	page := string(IndexHTML)

	required := []string{
		"Lite2API Quiet Control v3",
		"window.Lite2APIQuietControl",
		`--action: #4c8dff`,
		`--healthy: #42c77a`,
		`.nav{grid-template-columns:repeat(4,1fr)}`,
		`content: attr(data-label)`,
		`prefers-reduced-motion`,
		`id: "qcCommandDialog"`,
		`lite2api.sourcesPane`,
		`qc-route-draft`,
		`qc-sheet`,
	}
	for _, marker := range required {
		if !strings.Contains(page, marker) {
			t.Errorf("quiet-control page is missing %q", marker)
		}
	}

	if !strings.Contains(page, `class="chart-grid"`) ||
		!strings.Contains(page, `id="requestChart"`) ||
		!strings.Contains(page, `id="latencyChart"`) {
		t.Fatal("overview must keep both request and P95 chart canvases visible")
	}
	if strings.Contains(page, "qc-chart-disclosure") || strings.Contains(page, "overviewCharts") {
		t.Fatal("overview charts must not be hidden behind a persisted disclosure")
	}

	forbidden := []string{
		"Operations-first refinement",
		"Console 2.1: one semantic layer",
		"background:radial-gradient",
		"--accent:#47d39e",
	}
	for _, marker := range forbidden {
		if strings.Contains(page, marker) {
			t.Errorf("historical visual layer leaked into canonical output: %q", marker)
		}
	}

	if strings.Count(page, "<style>") != 1 || strings.Count(page, "</style>") != 1 {
		t.Fatal("canonical page must contain exactly one style element")
	}
	if !strings.HasSuffix(strings.TrimSpace(page), "</body></html>") {
		t.Fatal("quiet-control injection must preserve the final document structure")
	}
}

func TestBuildIndexHTMLReplacesStyleAndInjectsBehavior(t *testing.T) {
	base := []byte("<html><head><style>old</style></head><body><main>stable</main></body></html>")
	got := string(buildIndexHTML(base, []byte("new"), []byte("enhance()")))

	if strings.Contains(got, "old") {
		t.Fatal("historical style content was not replaced")
	}
	if !strings.Contains(got, "<style>\nnew\n</style>") {
		t.Fatalf("canonical style was not injected: %s", got)
	}
	if !strings.Contains(got, "<script>\nenhance()\n</script>\n</body>") {
		t.Fatalf("behavior layer was not injected before body close: %s", got)
	}
	if !strings.Contains(got, "<main>stable</main>") {
		t.Fatal("stable application markup must remain unchanged")
	}
}

func TestBuildIndexHTMLGracefullyHandlesMissingAnchors(t *testing.T) {
	base := []byte("<main>stable</main>")
	got := string(buildIndexHTML(base, []byte("new"), []byte("enhance()")))
	if got != string(base) {
		t.Fatalf("documents without style/body anchors must remain unchanged: %q", got)
	}
}
