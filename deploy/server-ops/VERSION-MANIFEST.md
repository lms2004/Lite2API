# Deployment Version Manifest

Verified on 2026-08-24 UTC.

| Item | Value |
|---|---|
| Server IPv4 | `64.83.25.68` |
| Hostname | `sub2api.foresights.top` |
| V2Fly core | `v5.52.0` |
| V2Fly release SHA-256 | `98b123c0f3ba1138eedc2be9b25935e5289236cb6800d1e6370a08d86e797177` |
| VLESS transport | WebSocket over TLS 1.2/1.3 |
| VLESS internal listener | `127.0.0.1:10086` |
| Lite2API listener | `127.0.0.1:45679` |
| Lite2API build | `deployed-20260824-4f10180cdfd8` |
| Lite2API binary SHA-256 | `d519123dfbe8c16adad5171f3443b6ae9a55fdb503db16d9e63640c56599257f` |
| Lite2API deployment | `deploy/install-lite2api-systemd.sh` (Go 1.24.4) |
| Admin UI | `2026.08.24-v14` (route strategy controls, account priority, bounded manual refresh, quota windows and cooldowns) |
| Lite2API adapter model | operation-aware dispatch + strict/priority/least-loaded/round-robin/sticky route selection + full failover chains |
| CLIProxyAPI listener | `127.0.0.1:45682` |
| CLIProxyAPI build | `v6.10.9-lite2api.5` / upstream `785b00c3127eea6aa207f1207ead8a2aa93690a3` + maintained quota, routing-reliability and auth-refresh patches |
| CLIProxyAPI binary SHA-256 | `84f84974ede18e005eb4df4210055eb7005b26843c166bd8d9028c12125cf0f5` |
| CLIProxyAPI deployment | `deploy/install-cliproxyapi-systemd.sh` + `cliproxyapi.service` |
| CLIProxyAPI account routing | `round-robin`; higher numeric priority first; same-priority rotation; request-level retry disabled so Lite2API owns retry/failover |
| Account quota snapshot | in-memory only; Claude response windows; Codex + Gemini/Antigravity official quota APIs with 10-minute page-demand TTL; bounded manual refresh; model cooldown fallback |
| Nginx | Ubuntu package `1.24.x` |
| TLS issuer | Let's Encrypt |
| TLS certificate expiry | `2026-11-14 02:28:59 UTC` |
| Congestion control | `bbr` / `fq` |

The distro-provided V2Ray 4.x binary remains at `/usr/bin/v2ray` as an offline
rollback option. Production uses `/usr/local/lib/v2ray-current/v2ray`.
