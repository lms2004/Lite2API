# Lite2API isolated deployment report

- Repository: lms2004/Lite2API
- Code baseline: 0fbc955393e079553bee84c4e37ddef257c8347c
- Workflow commit: c63a31f5240ff4e50566fafbe10f1b4cb81106a7
- Build command: go build ./cmd/lite2api
- Service: http://127.0.0.1:45679
- Verified endpoints: /health, /admin, /admin/api/session, /admin/api/state
- Browser viewport: 1440 x 1000

## Health response
```json

```

## Server log tail
```text
{"time":"2026-08-19T02:28:07.107920694Z","level":"INFO","msg":"configuration loaded","accounts":4,"models":3}
{"time":"2026-08-19T02:28:07.108035021Z","level":"INFO","msg":"lite2api listening","address":"127.0.0.1:45679"}
{"time":"2026-08-19T02:28:09.109618388Z","level":"WARN","msg":"upstream capability discovery finished with errors","error":"no model catalogs could be refreshed (1 endpoints unavailable)"}
```
