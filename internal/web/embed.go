package web

import (
	"bytes"
	_ "embed"
)

//go:embed index.html
var legacyIndexHTML []byte

//go:embed quiet-control-core.css
var quietControlCoreCSS []byte

//go:embed quiet-control-pages.css
var quietControlPagesCSS []byte

//go:embed quiet-control-enhancements.css
var quietControlEnhancementsCSS []byte

//go:embed quiet-control.js
var quietControlJS []byte

// IndexHTML is the complete self-contained admin page served by Lite2API.
//
// The legacy document still owns the stable DOM, API calls, and runtime
// behavior. At build time we replace its historical style cascade with the
// canonical Quiet Control layers and inject the non-invasive interaction
// enhancement script. The server therefore keeps returning one HTML asset and
// the existing CSP can remain unchanged.
var IndexHTML = buildIndexHTML(
	legacyIndexHTML,
	bytes.Join([][]byte{
		quietControlCoreCSS,
		quietControlPagesCSS,
		quietControlEnhancementsCSS,
	}, []byte("\n")),
	quietControlJS,
)

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
