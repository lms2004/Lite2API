# Deployment Version Manifest

Verified on 2026-08-16 UTC.

| Item | Value |
|---|---|
| Server IPv4 | `64.83.25.68` |
| Hostname | `sub2api.foresights.top` |
| V2Fly core | `v5.52.0` |
| V2Fly release SHA-256 | `98b123c0f3ba1138eedc2be9b25935e5289236cb6800d1e6370a08d86e797177` |
| VLESS transport | WebSocket over TLS 1.2/1.3 |
| VLESS internal listener | `127.0.0.1:10086` |
| Lite2API listener | `127.0.0.1:45679` |
| Lite2API build | `deployed-20260816-5c6c682d2c78` |
| Lite2API binary SHA-256 | `a9374f5e2149b1b6125c4396d63b51c49af974636abf331417e52d55109812de` |
| Lite2API deployment | `deploy/install-lite2api-systemd.sh` (Go 1.24.4) |
| Admin UI | `2026.08.16-r9` (multi-provider quota windows, balances, cooldowns, page-aware refresh) |
| Lite2API adapter model | operation-aware dispatch + 60-second on-demand probe cache |
| CLIProxyAPI listener | `127.0.0.1:45682` |
| CLIProxyAPI build | `v6.10.9-lite2api.4` / upstream `785b00c3127eea6aa207f1207ead8a2aa93690a3` + `deploy/patches/cliproxyapi-quota-snapshot.patch` |
| CLIProxyAPI binary SHA-256 | `e92120376e56b015bcff5e90ce8f288090264b73040c69acdcd33b9f2f88ab86` |
| CLIProxyAPI deployment | `deploy/install-cliproxyapi-systemd.sh` + `cliproxyapi.service` |
| Account quota snapshot | in-memory only; Claude response windows; Codex + Gemini/Antigravity official quota APIs with 10-minute page-demand TTL; model cooldown fallback |
| Nginx | Ubuntu package `1.24.x` |
| TLS issuer | Let's Encrypt |
| TLS certificate expiry | `2026-11-14 02:28:59 UTC` |
| Congestion control | `bbr` / `fq` |

The distro-provided V2Ray 4.x binary remains at `/usr/bin/v2ray` as an offline
rollback option. Production uses `/usr/local/lib/v2ray-current/v2ray`.
