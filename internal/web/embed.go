package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/base64"
)

//go:embed app.html
var appHTML []byte

//go:embed app.css
var appCSS []byte

//go:embed app-core.js
var appCoreJS []byte

//go:embed app.js
var appJS []byte

//go:embed assets/model-icons/*.svg assets/model-icons/*.png
var modelIconFS embed.FS

type officialIconAsset struct {
	token string
	path  string
	mime  string
}

var officialIconAssets = []officialIconAsset{
	{token: "__OFFICIAL_ICON_OPENAI__", path: "assets/model-icons/openai.svg", mime: "image/svg+xml"},
	{token: "__OFFICIAL_ICON_CLAUDE__", path: "assets/model-icons/claude.svg", mime: "image/svg+xml"},
	{token: "__OFFICIAL_ICON_GEMINI__", path: "assets/model-icons/gemini-api-logo.svg", mime: "image/svg+xml"},
	{token: "__OFFICIAL_ICON_ANTIGRAVITY__", path: "assets/model-icons/antigravity.png", mime: "image/png"},
	{token: "__OFFICIAL_ICON_GPT_5_6_SOL__", path: "assets/model-icons/gpt-5.6-sol.png", mime: "image/png"},
	{token: "__OFFICIAL_ICON_GPT_5_6_TERRA__", path: "assets/model-icons/gpt-5.6-terra.png", mime: "image/png"},
	{token: "__OFFICIAL_ICON_GPT_5_6_LUNA__", path: "assets/model-icons/gpt-5.6-luna.png", mime: "image/png"},
	{token: "__OFFICIAL_ICON_GPT_5_5__", path: "assets/model-icons/gpt-5.5.png", mime: "image/png"},
	{token: "__OFFICIAL_ICON_GPT_5_4__", path: "assets/model-icons/gpt-5.4.png", mime: "image/png"},
	{token: "__OFFICIAL_ICON_GPT_5_4_MINI__", path: "assets/model-icons/gpt-5.4-mini.png", mime: "image/png"},
	{token: "__OFFICIAL_ICON_GPT_5_3_CODEX__", path: "assets/model-icons/gpt-5.3-codex.png", mime: "image/png"},
	{token: "__OFFICIAL_ICON_GPT_IMAGE_2__", path: "assets/model-icons/gpt-image-2.png", mime: "image/png"},
	{token: "__OFFICIAL_ICON_GPT_OSS_120B__", path: "assets/model-icons/gpt-oss-120b.png", mime: "image/png"},
}

var canonicalCSS = injectOfficialIcons(appCSS)
var canonicalScript = bytes.Join([][]byte{bytes.TrimSpace(appCoreJS), bytes.TrimSpace(appJS)}, []byte("\n"))

// IndexHTML is the complete canonical Lite2API management application. The
// server still ships a self-contained document, but every runtime surface now
// comes from the app sources above rather than ordered native-v* overrides.
var IndexHTML = buildApp(appHTML, canonicalCSS, canonicalScript)
var IndexHTMLGzip = gzipBytes(IndexHTML)

// ScriptCSPSource pins the exact embedded controller. Dynamic HTML can never
// opt itself into script execution because the server no longer needs
// script-src 'unsafe-inline'.
var ScriptCSPSource = cspHash(canonicalScript)

func injectOfficialIcons(css []byte) []byte {
	result := append([]byte(nil), css...)
	for _, asset := range officialIconAssets {
		token := []byte(asset.token)
		if bytes.Count(result, token) != 1 {
			panic("canonical stylesheet must reference each official icon exactly once: " + asset.token)
		}
		data, err := modelIconFS.ReadFile(asset.path)
		if err != nil {
			panic("read official icon " + asset.path + ": " + err.Error())
		}
		uri := []byte("data:" + asset.mime + ";base64," + base64.StdEncoding.EncodeToString(data))
		result = bytes.Replace(result, token, uri, 1)
	}
	return result
}

func buildApp(html, css, script []byte) []byte {
	page := append([]byte(nil), html...)
	cssSlot := []byte("/*__APP_CSS__*/")
	jsSlot := []byte("/*__APP_JS__*/")
	if bytes.Count(page, cssSlot) != 1 || bytes.Count(page, jsSlot) != 1 {
		panic("canonical admin page must contain exactly one CSS and JavaScript slot")
	}
	page = bytes.Replace(page, cssSlot, bytes.TrimSpace(css), 1)
	page = bytes.Replace(page, jsSlot, bytes.TrimSpace(script), 1)
	return page
}

func cspHash(script []byte) string {
	digest := sha256.Sum256(bytes.TrimSpace(script))
	return "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
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
