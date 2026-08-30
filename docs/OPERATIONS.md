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
curl -fsS http://127.0.0.1:45679/livez
curl -fsS http://127.0.0.1:45679/readyz  # 启用并配置上游后再要求成功
docker stats --no-stream lite2api
```

`/health` 将进程存活（`liveness: ok`）与模型访问状态分开：无近期样本为 `unknown`，最近请求验证通过为 `ready`，部分失败为 `degraded`，路由目标缺失或无可尝试目标为 `unavailable`。只有 `unavailable` 返回 HTTP 503，避免把无流量启动期误判为进程故障。

systemd 生产主机使用统一检查脚本：

```bash
sudo ./deploy/server-ops/check-services.sh
systemctl status lite2api cliproxyapi
journalctl -u lite2api -u cliproxyapi --since today
```

Lite2API 本体使用可重建、自动回滚的安装流程：脚本会创建缺失的用户、目录、环境文件和初始配置，从当前 `HEAD` 的 detached worktree 构建，验证配置后再切换服务：

```bash
sudo ./deploy/install-lite2api-systemd.sh
```

服务器若有多个 Go 版本，可通过 `GO_BIN=/absolute/path/to/go` 指定工具链；部署严格要求 Go `1.26.5`（与 Docker/CI 相同），升级工具链必须同时修改安装器、容器、CI 和版本证据。dirty/untracked 文件不会进入二进制；失败会恢复旧 binary/unit 和服务启用状态，成功后打印本机 rollback snapshot 路径。

CLIProxyAPI 的固定版本安装或升级流程：

```bash
git submodule update --init third_party/cliproxyapi
sudo ./deploy/install-cliproxyapi-systemd.sh
```

安装器具有幂等性：已有密钥和 OAuth 凭据不会被轮换或删除；固定上游提交在隔离 worktree 中重放三组补丁，随后执行回环健康检查及两条鉴权检查。失败会恢复二进制、unit、配置、两个环境文件与服务状态。

Nginx 与应用的管理来源名单只维护一份。修改 `/etc/lite2api/lite2api.env` 中的 `LITE2API_ADMIN_ALLOWED_CIDRS` 后执行：

```bash
sudo ./deploy/render-nginx-admin-allowlist.sh
sudo systemctl reload nginx
```

脚本拒绝公网 `/0`、符号链接和缺失 loopback 的列表，原子生成 `/etc/nginx/snippets/lite2api-admin-allowlist.conf`，并在保留新文件前运行 `nginx -t`。`check-services.sh` 会验证 Nginx 与应用列表完全一致。

## 更新与回滚

配置更新由程序先验证，再以临时文件 + `fsync` + 原子 rename 保存。Compose 更新前必须同时给旧镜像打不可变回滚标签，并导出命名卷；仅记录 image ID 或备份仓库内 `data/` 都不能恢复实际命名卷：

```bash
release_id=$(date -u +%Y%m%dT%H%M%SZ)
docker image tag lite2api:local "lite2api:rollback-$release_id"
docker volume ls --filter label=com.docker.compose.volume=lite2api-data
# 将上一步显示的精确卷名以只读方式导出到加密/异地备份介质。
docker compose build
docker compose up -d
curl -fsS http://127.0.0.1:45679/health
```

镜像升级失败时无需编辑 YAML：`LITE2API_IMAGE="lite2api:rollback-$release_id" docker compose up -d --no-build`。配置回滚必须恢复命名卷中的 `/app/data/config.json`、`client_keys.json` 和相关日志状态，再重建容器；仓库路径 `data/config.json` 不参与当前 Compose。

systemd 安装器在切换前创建本机短期 rollback snapshot，并在健康失败时自动恢复。成功安装输出 snapshot 路径；它用于快速升级回退，不替代异地备份。Lite2API 的动态配置、Key 元数据和日志仍在 `/etc/lite2api/` 原子更新；目录使用 `root:lite2api 1770` sticky 边界，服务拥有 `config.json`，但不能替换 root 拥有的 `lite2api.env`。

## 备份

备份至少应覆盖：

- Docker 命名卷 `lite2api-data` 中的 `/app/data/config.json`
- 同一命名卷中的 `/app/data/client_keys.json`（仅包含摘要，但丢失后所有托管 Key 都会失效）
- `.env`
- Compose 渠道运行目录 `channels/runtime/`（Gemini Cookie、CLIProxyAPI OAuth 凭据与渠道私有配置）
- 已创建的 Grok 命名卷 `lite2api-grok-data` 与 `lite2api-grok-quality`
- systemd 部署的 `/etc/lite2api/`（含 `client_keys.json` 与有界日志）、`/etc/cliproxyapi/` 和 `/var/lib/cliproxyapi/auths/`

备份必须在产生端加密后发送到异地主机或对象存储，设置 retention，并定期恢复到临时目录演练；不要在同一台服务器长期堆放明文 tar。程序没有数据库。运行统计在内存中，重启丢失，不属于需要恢复的业务状态。

Compose 提供无明文临时归档的 age 快照与流式验证脚本（`age` 私钥应保存在另一台主机或硬件介质）：

```bash
LITE2API_DATA_VOLUME=精确命名卷 \
  ./deploy/snapshot-compose-state.sh /mnt/offsite/lite2api-$(date -u +%Y%m%d) age1...
./deploy/verify-compose-snapshot.sh /mnt/offsite/lite2api-YYYYMMDD
```

快照脚本以只读卷、无网络、固定 Alpine digest 和仅 `DAC_READ_SEARCH` 能力导出，分别加密 Lite2API 卷、存在的 Grok 卷、存在的 `channels/runtime/` 与 `.env`；目标已存在时拒绝覆盖，状态树中出现符号链接也会失败。多 Compose 项目产生同名标签时，脚本拒绝猜测，需用 `LITE2API_DATA_VOLUME`、`GROK2API_DATA_VOLUME`、`GROK2API_QUALITY_VOLUME` 指定精确卷名。验证脚本对白名单内的密文清单做摘要校验，流式解密每个 tar，并确认必需环境密钥满足强度且不是示例占位符，不把密钥值打印到终端。为得到跨文件一致的灾备点，应先停写或停容器再快照；生产恢复仍需在停流后由管理员显式执行，避免自动脚本误覆盖活跃卷。

systemd 状态使用同样的流式 age 边界：

```bash
sudo ./deploy/server-ops/backup-configs.sh /mnt/offsite/lite2api-systemd-$(date -u +%Y%m%d) age1...
./deploy/server-ops/verify-systemd-backup.sh /mnt/offsite/lite2api-systemd-YYYYMMDD
```

旧的同机 `/root/server-backups/*.tar.gz` 明文归档方式已移除；新脚本拒绝符号链接、从不创建明文 tar、拒绝覆盖既有目标。

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
- OAuth 多账号看似没有自动切换：先确认同一 provider 至少有两个支持该模型且状态为 `ready` 的凭据。认证账号优先级是高值优先；最高优先级账号健康时不会使用低优先级账号，同级是否平均分配由“同级选号”决定。会话粘性会让同一会话保持账号，但鉴权、额度、冷却或上游故障后仍会重选。结合账号卡片最近约 200 分钟的成功/失败数、`next_retry_after` 和服务日志判断是固定首选、会话粘性、模型不兼容还是实际故障转移。
- 调整 OAuth 优先级后仍命中旧账号：刷新账号列表确认持久化值；Gemini 虚拟项目账号会把优先级写回父凭据并同步到项目条目。若目标账号处于冷却或不支持请求模型，调度器会跳过它，即使它的数值最高。
- 外层路由没有按连接优先级选号：显式 `targets[]` 默认为严格手动顺序。到“模型路由”把“账号策略”改为“账号优先级”后，才会按 Lite 连接 priority **小值优先**；同值继续使用拖动顺序。也可选最少负载、轮询或会话粘滞。明确拒绝的 401/402/403/429、显式目标 404 和可证明尚未写入上游的连接错误可以排除当前目标后继续；任意上游 POST 的 408/409/425/5xx 或写入后传输错误都可能已经产生结果或计费（包括 Embeddings、Rerank），会返回 `uncertain_submission` 而不自动重放。
- “同级选号”保存失败：CLIProxyAPI 必须能写自己的单个配置文件。Compose 应把 `/CLIProxyAPI/config.yaml` 以 `rw` 挂载；systemd 配置应为 `root:cliproxyapi 0660`，并在沙箱中仅放行 `/etc/cliproxyapi/config.yaml`。仓库安装器会设置这两个边界，并在升级时保留已选的 `round-robin` / `fill-first`。不要扩大为整个 `/etc` 可写。
- Claude 额度显示“等待观测”：先确认已有真实 Claude 请求成功；Claude 快照等待真实响应。Codex、Gemini CLI 或 Antigravity 显示“正在按需同步”时，保持账号页打开一个刷新周期；官方查询异步执行，并按凭据缓存 10 分钟。不要把未知当作 0%，也不要开启 `passthrough-headers`。
- 额度显示“数据已过期”：账号可能长时间没有流量；这是观测新鲜度提示，不会单独触发停用。真正的 429/冷却仍由适配器健康状态处理。
- 官方额度查询失败不会阻断推理或把账号停用；失败凭据最早 1 分钟后重试，成功凭据 10 分钟后才允许再次查询。先检查 OAuth Token、项目 ID、账号代理和提供方状态，不要通过缩短页面刷新间隔放大故障。
- 适配器显示“待授权”：进程和管理鉴权正常，但模型列表为空；完成一次 OAuth 或 setup-token 登录。
- 适配器显示“未运行”：固定回环端口探针失败；只有配置账号并处于 `ready` 时才允许承载流量。
- Gemini/Grok Web Cookie 或 SSO：只在浏览器本地整理，再写入对应隔离适配器。未配置时保持服务停止；不要为了“目录全绿”启动无凭据的常驻进程。
- 路由存在但返回 `503`：除健康和并发外，还要检查账号 `operations` 是否包含本次请求类型、显式 `targets[]` 是否仍有后备目标，以及 OAuth 池内是否至少有一个支持该模型且未冷却的凭据。

## 容量建议

当前 2 核、2 GiB 服务器使用 256 MiB 容器内存、1.5 CPU、32 个入口并发、8 MiB 请求体、64 个单上游连接和 192 MiB Go 软内存限制。账号 `concurrency` 应按上游真实允许值设置；总入口 `max_inflight_requests` 用于保护本机，而不是扩大上游配额。

CLIProxyAPI 模板把 `max-retry-credentials` 设为 `0`，含义是一次执行先尝试所有当前符合模型与优先级规则的凭据，而不是只试前三个；被禁用或冷却的凭据不会产生无效调用。托管模板同时使用 `request-retry: 0`：完成一轮后立即把明确错误交回 Lite2API，让外层 `targets[]` 继续切换，不在适配器内部等待后掩盖跨渠道 failover。需要单渠道短暂等待重试时可显式调高，但不会改变高优先级优先和同级选号规则。

单机 Key 鉴权和限流应保持内存实现。只有扩展为多个 Lite2API 实例时，才启用 Redis 状态实现；Redis 不负责保存 Key 明文。

适配器状态探针同样保持无守护轮询：只有管理端读取目录才触发，相同身份 singleflight 合并、最多 4 路并行、整体预算 750 ms；成功缓存 60 秒，失败最多 5 秒。带 Key 的适配器只有鉴权模型目录非空时才 ready。不要为单机增加数据库、消息队列、服务注册中心或独立监控 Agent。

请求明细只保留最近 512 条记录在内存；每条记录只包含路由、状态、耗时、模态、字节数和上游返回的 Token usage，不保存正文。运行趋势另有独立的 1 分钟数据点环形缓冲，默认保留最近 7 天；超过容量后覆盖最早数据。管理台通过 `/admin/api/trends?range=1h|6h|24h|3d|7d` 读取趋势，健康判断仍严格使用最近 5 分钟。摘要日志默认写到配置文件同目录的 `request.log`，单文件 8 MiB、保留 2 个备份，达到上限后循环覆盖；可用 `server.request_log_path`、`server.request_log_max_bytes` 和 `server.request_log_backups` 调整。

OAuth 账号卡片中的 Prompt usage 只来自 CLIProxyAPI 对真实 provider `usage.Detail` 的最新观测：输入字段标为“注入后输入 / 上游 input tokens”，不能解读为纯 system prompt 或纯注入长度。快照仅驻留进程内，账号禁用或删除时清理；原始 prompt、响应正文和凭据不会写入 auth 持久化 JSON。无真实观测时管理台显示“暂无真实 usage”，不会按请求字符串估算 token。

安装器创建的 rollback snapshot 只保留升级前文件并留在本机；应按运维周期清理，不能替代加密异地备份。额度快照只在内存中存在，不属于备份或恢复范围。
