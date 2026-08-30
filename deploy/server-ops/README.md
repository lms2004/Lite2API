# Server Operations Guide

Last verified: 2026-08-30 UTC

This directory contains host-neutral production checks. Public hostnames, IP
addresses, certificate paths and VPN policy belong in the host's protected
environment/configuration management, not in this repository.

## Architecture

```text
Internet
  -> TLS reverse proxy
       -> public /v1 data plane -> Lite2API 127.0.0.1:45679
       -> allowlisted admin      -> Lite2API 127.0.0.1:45679
                                      -> CLIProxyAPI 127.0.0.1:45682
```

Lite2API and CLIProxyAPI are independent, unprivileged systemd services. The
reverse proxy is the only public listener. The application repeats the admin
CIDR check, so a reverse-proxy mistake does not silently expose the admin API.

## Rebuildable installation

```bash
cd /path/to/Lite2API
git submodule sync --recursive
git submodule update --init third_party/cliproxyapi
sudo ./deploy/install-lite2api-systemd.sh
sudo ./deploy/install-cliproxyapi-systemd.sh
sudo ./deploy/render-nginx-admin-allowlist.sh
sudo systemctl reload nginx
sudo ./deploy/server-ops/check-services.sh
```

The Lite2API installer provisions a missing service user, `/etc/lite2api`, a
private environment file and an initial configuration. Both installers build
from detached worktrees at selected commits; dirty and untracked checkout files
cannot enter a labeled release. CLIProxyAPI additionally reapplies the three
maintained patches to its fixed upstream revision. Each installer snapshots the
files it will replace and automatically restores them if health validation
fails. Successful installs print the local rollback snapshot path.

Sensitive state remains outside Git:

- `/etc/lite2api/`: `root:lite2api 1770`; sticky ownership permits atomic service-owned config/key/log updates while protecting the root-owned environment entry.
- `/etc/lite2api/lite2api.env`: gateway, admin and upstream keys.
- `/etc/lite2api/config.json`: desired routing configuration.
- `/etc/cliproxyapi/cliproxyapi.env`: adapter management credential.
- `/var/lib/cliproxyapi/auths/`: OAuth account credentials.
- `/var/lib/lite2api-rollbacks/` and `/var/lib/cliproxyapi-rollbacks/`: root-only, short-lived local upgrade snapshots kept outside service-writable state.

Local rollback snapshots are not backups. Use encrypted off-host storage and
regular restore drills as described in `docs/OPERATIONS.md`.

For systemd state, `backup-configs.sh OUTPUT_DIRECTORY AGE_RECIPIENT` streams
the selected files directly into age encryption; `verify-systemd-backup.sh`
checks the encrypted archive without writing a plaintext tar. Copy the snapshot
off-host and apply retention in the destination system.

## One-source admin allowlist

Maintain admin networks only in
`/etc/lite2api/lite2api.env`:

```text
LITE2API_ADMIN_ALLOWED_CIDRS=127.0.0.0/8,::1/128,VPN_EGRESS/32
```

`render-nginx-admin-allowlist.sh` parses that value without sourcing the file,
rejects `/0`, symlinks and missing loopback networks, atomically renders the
Nginx snippet, and runs `nginx -t`. The operator reviews the result before
reloading Nginx. `check-services.sh` then requires an exact set match between
the snippet and application environment.

## Portable service verification

The check script always validates local service/config/authentication and
redaction contracts. Public DNS, TLS and transport tuning are optional inputs:

```bash
sudo env \
  LITE2API_PUBLIC_ORIGIN=https://api.example.com \
  LITE2API_PUBLIC_API_BASE=https://api.example.com/lite/v1 \
  LITE2API_EXPECTED_PUBLIC_IP=203.0.113.10 \
  LITE2API_TLS_CERTIFICATE=/etc/letsencrypt/live/api.example.com/cert.pem \
  LITE2API_REQUIRE_BBR=true \
  LITE2API_REQUIRE_DATA_PLANE_READY=true \
  LITE2API_REQUIRE_OAUTH_READY=true \
  ./deploy/server-ops/check-services.sh
```

Omit an optional variable when that component is intentionally absent. A fresh,
fail-closed Lite2API install is live while all example accounts remain disabled;
only `LITE2API_REQUIRE_DATA_PLANE_READY=true` requires traffic-ready routes. A
fresh OAuth adapter may likewise be healthy with an empty credential pool; only
`LITE2API_REQUIRE_OAUTH_READY=true` requires at least one ready credential.
Long-lived keys are passed to curl through `0600` header files, never command
arguments.

Routine diagnostics:

```bash
systemctl status lite2api cliproxyapi
journalctl -u lite2api -u cliproxyapi --since today
ss -lntp
curl -fsS http://127.0.0.1:45679/health
```

## V2Fly helper

`update-v2ray-core.sh` is optional and independent of Lite2API. It accepts an
explicit semantic release tag or resolves the latest official release, verifies
the published SHA-256 digest, validates the existing V2Fly configuration and
any same-version directory byte-for-byte, serializes concurrent updates,
switches a versioned symlink, and restores the prior pointer if restart fails.
Do not treat it as a prerequisite for hosts using another VPN or no VPN.
