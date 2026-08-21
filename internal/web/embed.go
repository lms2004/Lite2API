package web

import (
	"bytes"
	"compress/gzip"
	_ "embed"
)

//go:embed index.html
var legacyIndexHTML []byte

//go:embed native-v5.css
var nativeV5CSS []byte

//go:embed native-v6.css
var nativeV6CSS []byte

//go:embed native-v7.css
var nativeV7CSS []byte

//go:embed native-v8.css
var nativeV8CSS []byte

//go:embed native-v9.css
var nativeV9CSS []byte

//go:embed native-v9-refine.css
var nativeV9RefineCSS []byte

//go:embed native-v10.css
var nativeV10CSS []byte

//go:embed native-v10-dialog-polish.css
var nativeV10DialogPolishCSS []byte

//go:embed native-theme.css
var nativeThemeCSS []byte

//go:embed native-v12.css
var nativeV12CSS []byte

//go:embed native-v5.js
var nativeV5JS []byte

//go:embed native-v6.js
var nativeV6JS []byte

//go:embed native-v7.js
var nativeV7JS []byte

//go:embed native-v9.js
var nativeV9JS []byte

//go:embed native-v10.js
var nativeV10JS []byte

//go:embed native-v10-quota.js
var nativeV10QuotaJS []byte

//go:embed native-v10-provider-fixes.js
var nativeV10ProviderFixesJS []byte

//go:embed native-v10-provider-methods.js
var nativeV10ProviderMethodsJS []byte

//go:embed native-theme.js
var nativeThemeJS []byte

//go:embed native-v12-motion.js
var nativeV12MotionJS []byte

//go:embed native-account-status.js
var nativeAccountStatusJS []byte

//go:embed native-route-compat.js
var nativeRouteCompatJS []byte

//go:embed native-render-perf.js
var nativeRenderPerfJS []byte

//go:embed native-adapter-clarity.js
var nativeAdapterClarityJS []byte

//go:embed native-v5-shell.html
var nativeV5Shell []byte

//go:embed native-v5-monitor.html
var nativeV5Monitor []byte

//go:embed native-v5-routes.html
var nativeV5Routes []byte

//go:embed native-v5-accounts.html
var nativeV5Accounts []byte

//go:embed native-v5-keys.html
var nativeV5Keys []byte

//go:embed native-v10-account-dialogs.html
var nativeV10AccountDialogs []byte

// IndexHTML is the complete self-contained admin page served by Lite2API.
// Stable gateway handlers remain in the legacy document. Compile-time native
// markup replaces the task surfaces before embedding. Native v12 keeps quota,
// call volume, speed, channel quality, and provider-specific account onboarding
// the primary product workflows and applies one coherent console design.
var IndexHTML = buildNativeIndexHTML(legacyIndexHTML)
var IndexHTMLGzip = gzipBytes(IndexHTML)

func buildNativeIndexHTML(base []byte) []byte {
	page := append([]byte(nil), base...)

	page = replaceRange(page, []byte(`<div class="app-shell">`), []byte(`<section id="view-accounts"`), bytes.TrimSpace(nativeV5Shell))
	page = replaceRange(page, []byte(`<section id="view-accounts"`), []byte(`<section id="view-keys"`), bytes.TrimSpace(nativeV5Accounts))
	page = replaceRange(page, []byte(`<section id="view-keys"`), []byte(`<section id="view-routes"`), bytes.TrimSpace(nativeV5Keys))
	page = replaceRange(page, []byte(`<section id="view-routes"`), []byte(`<section id="view-monitor"`), bytes.TrimSpace(nativeV5Routes))
	page = replaceRange(page, []byte(`<section id="view-monitor"`), []byte(`<section id="view-prompt-test"`), bytes.TrimSpace(nativeV5Monitor))
	page = replaceRange(page, []byte(`<dialog id="quickAuthDialog"`), []byte(`<dialog id="exportDialog"`), bytes.TrimSpace(nativeV10AccountDialogs))

	page = bytes.Replace(page, []byte(`const UI_BUILD='2026.08.16-r11'`), []byte(`const UI_BUILD='2026.08.20-v12'`), 1)
	page = bytes.Replace(page, []byte(`<meta name="theme-color" content="#080c12">`), []byte(`<meta name="theme-color" content="#071421">`), 1)

	css := bytes.Join([][]byte{nativeV5CSS, nativeV6CSS, nativeV7CSS, nativeV8CSS, nativeV9CSS, nativeV9RefineCSS, nativeV10CSS, nativeV10DialogPolishCSS, nativeThemeCSS, nativeV12CSS}, []byte("\n"))
	// Account controls load first so they remain available even if a later
	// visual enhancement is unavailable in an older browser.
	js := bytes.Join([][]byte{nativeAccountStatusJS, nativeRouteCompatJS, nativeRenderPerfJS, nativeV5JS, nativeV6JS, nativeV7JS, nativeV9JS, nativeV10JS, nativeV10QuotaJS, nativeV10ProviderFixesJS, nativeV10ProviderMethodsJS, nativeAdapterClarityJS, nativeThemeJS, nativeV12MotionJS}, []byte("\n"))
	return buildIndexHTML(page, css, js)
}

func gzipBytes(data []byte) []byte {
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		return nil
	}
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return nil
	}
	if err := writer.Close(); err != nil {
		return nil
	}
	return buffer.Bytes()
}

func replaceRange(page, startMarker, endMarker, replacement []byte) []byte {
	start := bytes.Index(page, startMarker)
	if start < 0 {
		return page
	}
	endOffset := bytes.Index(page[start:], endMarker)
	if endOffset < 0 {
		return page
	}
	end := start + endOffset
	next := make([]byte, 0, len(page)-(end-start)+len(replacement)+1)
	next = append(next, page[:start]...)
	next = append(next, replacement...)
	next = append(next, '\n')
	next = append(next, page[end:]...)
	return next
}

// buildIndexHTML keeps the final document self-contained: one canonical style
// element and one enhancement script inserted before </body>.
func buildIndexHTML(base, css, js []byte) []byte {
	page := append([]byte(nil), base...)

	styleOpen := bytes.Index(page, []byte("<style>"))
	styleClose := bytes.Index(page, []byte("</style>"))
	if styleOpen >= 0 && styleClose > styleOpen {
		contentStart := styleOpen + len("<style>")
		next := make([]byte, 0, len(page)-styleClose+contentStart+len(css)+2)
		next = append(next, page[:contentStart]...)
		next = append(next, '\n')
		next = append(next, bytes.TrimSpace(css)...)
		next = append(next, '\n')
		next = append(next, page[styleClose:]...)
		page = next
	}

	bodyClose := bytes.LastIndex(page, []byte("</body>"))
	if bodyClose < 0 || len(bytes.TrimSpace(js)) == 0 {
		return page
	}

	script := make([]byte, 0, len(js)+22)
	script = append(script, []byte("\n<script>\n")...)
	script = append(script, bytes.TrimSpace(js)...)
	script = append(script, []byte("\n</script>\n")...)

	next := make([]byte, 0, len(page)+len(script))
	next = append(next, page[:bodyClose]...)
	next = append(next, script...)
	next = append(next, page[bodyClose:]...)
	return next
}
