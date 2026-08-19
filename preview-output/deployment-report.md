# Lite2API GitHub Actions deployment report

- Repository: lms2004/Lite2API
- Code baseline: 0fbc955393e079553bee84c4e37ddef257c8347c
- Workflow commit: 13f7b80a2cd70cbf1d458f4e0fd337b0d444d34a
- Runner: Linux/X64
- Validation: go test ./... + go build ./cmd/lite2api
- Service: http://127.0.0.1:45679
- Browser: Playwright Chromium
- Viewport: 1440 x 1000

## Endpoint status

- /health: 503
- /admin: 200
- sample /v1/chat/completions: 200
- Playwright exit: 1

## Server log tail
```text
{"time":"2026-08-19T02:35:22.029439493Z","level":"INFO","msg":"configuration loaded","accounts":4,"models":3}
{"time":"2026-08-19T02:35:22.029537291Z","level":"INFO","msg":"lite2api listening","address":"127.0.0.1:45679"}
{"time":"2026-08-19T02:35:24.095705259Z","level":"INFO","msg":"upstream capabilities synchronized","accounts":1}
```
