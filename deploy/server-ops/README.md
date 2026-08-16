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
  |                                              |
  |                                              +-> OAuth adapter on 127.0.0.1:45682
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
| CLIProxyAPI OAuth adapter | `cliproxyapi.service` | `/etc/cliproxyapi/config.yaml` |
| Certificate renewal | `certbot.timer` | `/etc/letsencrypt/renewal/sub2api.foresights.top.conf` |

CLIProxyAPI 的首次安装和固定版本升级统一使用：

```bash
cd /root/Lite2API
git submodule update --init third_party/cliproxyapi
./deploy/install-cliproxyapi-systemd.sh
```

安装器校验固定提交，幂等应用仓库维护的额度快照补丁，保留既有密钥与 OAuth 凭据，更新二进制、配置和 systemd 单元，然后验证回环健康、管理鉴权与模型鉴权。不要直接从第三方 `main` 分支构建生产版本。安装过程不创建备份。

Lite2API 本体统一使用：

```bash
cd /root/Lite2API
./deploy/install-lite2api-systemd.sh
./deploy/server-ops/check-services.sh
```

安装器使用 Go 1.23+ 构建临时二进制，先执行 `-check-config`，再同步 systemd 单元并重启。按本轮明确要求，该流程不创建配置、凭据或旧二进制备份。

Sensitive files:

- `/etc/lite2api/lite2api.env`: API key, admin token and upstream variables.
- `/etc/cliproxyapi/cliproxyapi.env`: OAuth adapter management credential.
- `/var/lib/cliproxyapi/auths/`: OAuth account credentials created by completed logins.
- `/root/v2ray-client.json`: full VLESS client configuration.
- `/root/v2ray-share.txt`: importable VLESS URL.
- `/etc/letsencrypt/live/sub2api.foresights.top/privkey.pem`: TLS private key.

Do not copy these files into Git, tickets, chat rooms or public backups.

## Routine commands

```bash
/root/server-ops/check-services.sh
journalctl -u v2ray -u nginx -u lite2api -u cliproxyapi --since today
systemctl restart v2ray
systemctl reload nginx
systemctl reload lite2api
systemctl restart cliproxyapi
```

升级后运行完整检查脚本；它同时验证配置、目录操作类型、按需运行探针、两条适配器鉴权路径、多形态额度快照 schema 与脱敏边界、监听端口、资源占用和关键端点延迟。Codex 与 Google 系官方额度查询只在账号页读取时异步触发，成功后缓存 10 分钟；授权链接生成成功不代表账号已完成登录，`/v1/models` 为空时应先检查 `/var/lib/cliproxyapi/auths/` 是否已有有效凭据。

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
- V2Fly, Lite2API and CLIProxyAPI bind only to loopback.
- TLS certificates renew through `certbot.timer`; the deploy hook validates and
  reloads Nginx.
- Network tuning lives in `/etc/sysctl.d/90-v2ray-network.conf`.
- Congestion control is `bbr` with the `fq` queue discipline.
- A 1 GiB `/swapfile` is enabled and listed in `/etc/fstab`.

## Known application state

Lite2API has three enabled logical routing profiles backed by the local
CLIProxyAPI instance and four stable aliases: `gpt`, `gemini`, `claude` and
`grok`. All three accounts explicitly support Chat Completions, Responses and
Anthropic Messages; unsupported operations fail locally without waiting for the
queue timeout. OAuth credentials are displayed separately as a sanitized
authentication pool; adding Antigravity or another login does not duplicate a
routing profile. CLIProxyAPI is `ready` with 26 discovered models. AtomCode2API,
Grok2API and Gemini Web2API are installed catalog entries but currently stopped
and cannot receive traffic. They remain stopped until credentials are configured
to avoid wasting server memory.

Ubuntu kernel `6.8.0-137-generic` is installed but the host was last verified
running `6.8.0-48-generic`. A controlled reboot is required to load the newer
OS kernel; this is separate from the V2Fly 5.x application-core upgrade.

## Alternate admin egress

The admin allowlist includes `64.83.25.68/32` and `70.39.198.196/32`.
Both Nginx and Lite2API enforce the same list. If the alternate server IP
changes, update both layers; access fails closed until they match.
