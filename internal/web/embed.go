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

//go:embed apple-layout-v4.css
var appleLayoutV4CSS []byte

//go:embed quiet-control.js
var quietControlJS []byte

//go:embed apple-layout-v4.js
var appleLayoutV4JS []byte

// IndexHTML is the complete self-contained admin page served by Lite2API.
//
// index.html continues to own stable business markup and API behavior. The
// embedded style and behavior layers reshape that DOM into the current
// Apple-simple operator interface at build time, so the server still returns
// one self-contained HTML asset and the existing CSP remains valid.
var IndexHTML = buildIndexHTML(
	legacyIndexHTML,
	bytes.Join([][]byte{
		quietControlCoreCSS,
		quietControlPagesCSS,
		quietControlEnhancementsCSS,
		appleLayoutV4CSS,
	}, []byte("\n")),
	bytes.Join([][]byte{
		quietControlJS,
		appleLayoutV4JS,
	}, []byte("\n")),
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
