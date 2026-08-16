# Server Operations Guide

Last verified: 2026-08-16 UTC

This directory documents the production deployment on `64.83.25.68`. It is
kept outside the Lite2API Git repository so host-specific details and secret
file locations are not mixed into application source control.

## Architecture

```text
Internet
  |
  +-- TCP 80  -> Nginx -> HTTPS redirect / ACME challenge
  +-- TCP 443 -> Nginx TLS
  |               +-- /relay-<private-path> -> V2Fly VLESS/WS on 127.0.0.1:10086
  |               +-- /lite/v1/*          -> Lite2API on 127.0.0.1:45679
  |               +-- /lite-admin/*       -> Lite2API admin (allowlisted only)
  +-- TCP 39264 -> SSH
```

Public hostname: `sub2api.foresights.top`

- Lite2API base URL: `https://sub2api.foresights.top/lite/v1`
- Admin URL: `https://sub2api.foresights.top/lite-admin/`
- Health URL: `https://sub2api.foresights.top/health`
- VLESS share URL: `/root/v2ray-share.txt` (credential material, mode `0600`)

The admin page is intentionally available only from loopback or through the
server-side VLESS proxy. A normal public request must receive HTTP 403.

## Services and important files

| Component | Service | Configuration |
|---|---|---|
| Nginx/TLS | `nginx.service` | `/etc/nginx/sites-available/sub2api.conf` |
| V2Fly | `v2ray.service` | `/etc/v2ray/config.json` |
| Lite2API | `lite2api.service` | `/etc/lite2api/config.json` |
| Certificate renewal | `certbot.timer` | `/etc/letsencrypt/renewal/sub2api.foresights.top.conf` |

Sensitive files:

- `/etc/lite2api/lite2api.env`: API key, admin token and upstream variables.
- `/root/v2ray-client.json`: full VLESS client configuration.
- `/root/v2ray-share.txt`: importable VLESS URL.
- `/etc/letsencrypt/live/sub2api.foresights.top/privkey.pem`: TLS private key.

Do not copy these files into Git, tickets, chat rooms or public backups.

## Routine commands

```bash
/root/server-ops/check-services.sh
/root/server-ops/backup-configs.sh
journalctl -u v2ray -u nginx -u lite2api --since today
systemctl restart v2ray
systemctl reload nginx
systemctl reload lite2api
```

Upgrade V2Fly to the latest official GitHub release:

```bash
/root/server-ops/update-v2ray-core.sh
```

Install a specific release:

```bash
/root/server-ops/update-v2ray-core.sh v5.52.0
```

The updater verifies the official SHA-256 digest, validates the production
configuration with the new binary, switches a versioned symlink, and rolls
back automatically if the service does not start.

## Security and networking

- UFW permits only TCP `39264`, `80`, and `443`.
- V2Fly and Lite2API bind only to loopback.
- TLS certificates renew through `certbot.timer`; the deploy hook validates and
  reloads Nginx.
- Network tuning lives in `/etc/sysctl.d/90-v2ray-network.conf`.
- Congestion control is `bbr` with the `fq` queue discipline.
- A 1 GiB `/swapfile` is enabled and listed in `/etc/fstab`.

## Known application state

Lite2API is deployed and authenticated, but the four example upstream accounts
are disabled because no upstream provider credentials were present on this
host. `/health` is expected to report `models: 0` until accounts are added.

Ubuntu kernel `6.8.0-137-generic` is installed but the host was last verified
running `6.8.0-48-generic`. A controlled reboot is required to load the newer
OS kernel; this is separate from the V2Fly 5.x application-core upgrade.
