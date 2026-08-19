package web

import (
	"bytes"
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

//go:embed native-v5.js
var nativeV5JS []byte

//go:embed native-v6.js
var nativeV6JS []byte

//go:embed native-v7.js
var nativeV7JS []byte

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

// IndexHTML is the complete self-contained admin page served by Lite2API.
//
// The legacy document remains the source of stable business functions and
// low-frequency dialogs. Native markup replaces the shell and four core views
// before embedding. Native v6 refines content hierarchy; v7 adds the
// capability-aware model and reasoning controls without changing route APIs;
// v8 standardizes interaction polish; v9 reconstructs the overview, route, and
// upstream information architecture around operational decisions.
var IndexHTML = buildNativeIndexHTML(legacyIndexHTML)

func buildNativeIndexHTML(base []byte) []byte {
	page := append([]byte(nil), base...)

	page = replaceRange(page, []byte(`<div class="app-shell">`), []byte(`<section id="view-accounts"`), bytes.TrimSpace(nativeV5Shell))
	page = replaceRange(page, []byte(`<section id="view-accounts"`), []byte(`<section id="view-keys"`), bytes.TrimSpace(nativeV5Accounts))
	page = replaceRange(page, []byte(`<section id="view-keys"`), []byte(`<section id="view-routes"`), bytes.TrimSpace(nativeV5Keys))
	page = replaceRange(page, []byte(`<section id="view-routes"`), []byte(`<section id="view-monitor"`), bytes.TrimSpace(nativeV5Routes))
	page = replaceRange(page, []byte(`<section id="view-monitor"`), []byte(`<section id="view-prompt-test"`), bytes.TrimSpace(nativeV5Monitor))

	page = bytes.Replace(page, []byte(`const UI_BUILD='2026.08.16-r11'`), []byte(`const UI_BUILD='2026.08.19-v9'`), 1)
	page = bytes.Replace(page, []byte(`<meta name="theme-color" content="#080c12">`), []byte(`<meta name="theme-color" content="#f3f3f5">`), 1)

	css := bytes.Join([][]byte{nativeV5CSS, nativeV6CSS, nativeV7CSS, nativeV8CSS, nativeV9CSS}, []byte("\n"))
	js := bytes.Join([][]byte{nativeV5JS, nativeV6JS, nativeV7JS}, []byte("\n"))
	return buildIndexHTML(page, css, js)
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
// element and one enhancement script inserted before </body>. It is also kept
// as a small generic helper for focused embedding tests.
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
