# Operations

## 上线检查

1. `server.listen` 保持 `127.0.0.1:45679`。
2. 网关 Key、管理员 Token、CLIProxyAPI Key 与管理 Key 使用相互独立的随机值。
3. 上游密钥放入 `.env` 或权限受限的 systemd EnvironmentFile，不要写进 JSON 或 Git。
4. 配置、环境文件与 OAuth 凭据目录分别限制为 `0600`/`0640` 和 `0700`。
5. `LITE2API_ADMIN_ALLOWED_CIDRS` 只包含 loopback 与实际 VPN 出口；`LITE2API_TRUSTED_PROXY_CIDRS` 只包含 Nginx 的直接来源。
6. Nginx TLS server 中引入 `deploy/nginx-subpath.conf`，并在 reload 前执行 `nginx -t`。
7. 只通过 Nginx HTTPS 暴露模型 API；管理页通过 SSH Tunnel/VPN 访问。
8. 检查 `ss -lntup` 与容器或 systemd 服务状态，Lite2API 和所有渠道适配器只能监听 loopback。
9. 先检查 `/health`、适配器管理鉴权和 `/v1/models`，再执行一条流式真实请求。
10. 公网验证 `/lite/v1/models` 无 Key 返回 401；非 VPN 来源访问 `/lite-admin/` 返回 403。

## 运行检查

```bash
docker compose ps
docker compose logs --tail=100 lite2api
curl -fsS http://127.0.0.1:45679/health
docker stats --no-stream lite2api
```

systemd 生产主机使用统一检查脚本：

```bash
sudo ./deploy/server-ops/check-services.sh
systemctl status lite2api cliproxyapi
journalctl -u lite2api -u cliproxyapi --since today
```

Lite2API 本体使用统一的无备份原子安装流程；脚本先构建临时二进制、验证生产配置，再覆盖二进制并重启服务：

```bash
sudo ./deploy/install-lite2api-systemd.sh
```

服务器若有多个 Go 版本，可通过 `GO_BIN=/absolute/path/to/go` 指定 Go 1.23 以上工具链。当前安装流程不会复制配置、凭据或旧二进制；这是本轮明确要求的部署方式。

CLIProxyAPI 的固定版本安装或升级流程：

```bash
git submodule update --init third_party/cliproxyapi
sudo ./deploy/install-cliproxyapi-systemd.sh
```

安装器具有幂等性：已有密钥和 OAuth 凭据不会被轮换或删除；二进制、配置与单元文件会被更新，随后执行回环健康检查及两条鉴权检查。

## 更新与回滚

配置更新由程序先验证，再以临时文件 + `fsync` + 原子 rename 保存。建议更新镜像前保留当前镜像 ID：

```bash
docker image inspect lite2api:local --format '{{.Id}}'
docker compose build
docker compose up -d
curl -fsS http://127.0.0.1:45679/health
```

镜像升级失败时，将 Compose 的 `image` 改回旧标签后重新 `up -d`。配置回滚只需恢复 `data/config.json` 并发送 HUP。

systemd 安装器按当前生产要求直接原子替换二进制和配置，不自动创建备份。只有管理员明确要求保留回滚点时，才单独运行 `/root/server-ops/backup-configs.sh`。

## 备份

如启用人工备份，应覆盖：

- Docker 命名卷 `lite2api-data` 中的 `/app/data/config.json`
- 同一命名卷中的 `/app/data/client_keys.json`（仅包含摘要，但丢失后所有托管 Key 都会失效）
- `.env`
- systemd 部署的 `/etc/lite2api/`、`/etc/cliproxyapi/` 和 `/var/lib/cliproxyapi/auths/`

程序没有数据库。运行统计在内存中，重启丢失，不属于需要恢复的业务状态。

## 故障判断

- `503 no healthy account...`：路由没有健康账号，或账号并发槽全部占满并超过等待时间。
- `429 gateway concurrency limit reached`：入口总并发保护生效。
- 账号显示 `circuit_open_until`：连续失败、鉴权错误或上游限流触发熔断。
- `upstream stream idle`：响应头成功，但上游在配置时间内没有继续产生数据。
- `401 invalid_api_key`：客户端 Key 无效、已过期、已禁用或已撤销。
- `403 model_not_allowed`：该 Key 的模型白名单不包含请求模型。
- `429 rate_limit_exceeded`：Key 的 RPM 已达到本分钟上限。
- `429 concurrency_limit_exceeded`：Key 的并发槽已满；流式请求会一直持有槽位。
- 配置重载失败：旧运行配置继续提供服务；修复 JSON 后再次 reload。
- `OAuth adapter is not configured`：CLIProxyAPI 管理 Key 未注入 Lite2API；重新执行固定版本 systemd 安装器或 Compose 渠道初始化，并运行鉴权健康检查。
- OAuth 授权链接返回 502/503：检查 `cliproxyapi.service`、`127.0.0.1:45682`、管理 Key 是否一致，以及适配器日志。
- 页面提示“认证成功”但路由档位数量未增加：这是正常行为。OAuth 登录写入“认证账号池”，已有 `cliproxy-oauth` 路由档位会复用新凭据；检查 `GET /admin/api/oauth/accounts` 的脱敏条目和 `ready` 状态，不要为每个登录复制路由配置。
- Claude 额度显示“等待观测”：先确认已有真实 Claude 请求成功；Claude 快照等待真实响应。Codex、Gemini CLI 或 Antigravity 显示“正在按需同步”时，保持账号页打开一个刷新周期；官方查询异步执行，并按凭据缓存 10 分钟。不要把未知当作 0%，也不要开启 `passthrough-headers`。
- 额度显示“数据已过期”：账号可能长时间没有流量；这是观测新鲜度提示，不会单独触发停用。真正的 429/冷却仍由适配器健康状态处理。
- 官方额度查询失败不会阻断推理或把账号停用；失败凭据最早 1 分钟后重试，成功凭据 10 分钟后才允许再次查询。先检查 OAuth Token、项目 ID、账号代理和提供方状态，不要通过缩短页面刷新间隔放大故障。
- 适配器显示“待授权”：进程和管理鉴权正常，但模型列表为空；完成一次 OAuth 或 setup-token 登录。
- 适配器显示“未运行”：固定回环端口探针失败；只有配置账号并处于 `ready` 时才允许承载流量。
- Gemini/Grok Web Cookie 或 SSO：只在浏览器本地整理，再写入对应隔离适配器。未配置时保持服务停止；不要为了“目录全绿”启动无凭据的常驻进程。
- 路由存在但返回 `503`：除健康和并发外，还要检查账号 `operations` 是否包含本次请求类型。

## 容量建议

当前 2 核、2 GiB 服务器使用 256 MiB 容器内存、1.5 CPU、32 个入口并发、8 MiB 请求体、64 个单上游连接和 192 MiB Go 软内存限制。账号 `concurrency` 应按上游真实允许值设置；总入口 `max_inflight_requests` 用于保护本机，而不是扩大上游配额。

单机 Key 鉴权和限流应保持内存实现。只有扩展为多个 Lite2API 实例时，才启用 Redis 状态实现；Redis 不负责保存 Key 明文。

适配器状态探针同样保持无守护轮询：只有管理端读取目录才触发，500 ms 超时、60 秒内存缓存。不要为单机增加数据库、消息队列、服务注册中心或独立监控 Agent。

安装脚本不会创建配置、凭据或旧二进制备份。额度快照只在内存中存在，不属于备份或恢复范围。
