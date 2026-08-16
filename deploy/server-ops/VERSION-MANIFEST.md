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
| Lite2API build | `deployed-2026-08-16` |
| Nginx | Ubuntu package `1.24.x` |
| TLS issuer | Let's Encrypt |
| TLS certificate expiry | `2026-11-14 02:28:59 UTC` |
| Congestion control | `bbr` / `fq` |

The distro-provided V2Ray 4.x binary remains at `/usr/bin/v2ray` as an offline
rollback option. Production uses `/usr/local/lib/v2ray-current/v2ray`.
