# 渠道适配层

Lite2API 核心不复制第三方项目的账号登录、Cookie 刷新或逆向协议代码。每个渠道作为独立进程暴露 OpenAI 兼容接口，核心只负责统一入口、密钥隔离、模型映射、并发、熔断、换号与统计。

每个账号同时声明 `adapter_id`、`instance_id` 和 `operations`。前两者用于稳定关联实现与本机实例，`operations` 用于在调度前按 Chat、Responses、Anthropic Messages、Embeddings、Images 或 Rerank 能力过滤。适配器“进程运行”不等于“可承载流量”：只有凭据、模型和 Lite2API 账号配置都就绪时状态才是 `ready`。

当前固定源码：

- `third_party/cliproxyapi`：Gemini CLI、Claude、OpenAI/Codex、Antigravity 的 OAuth/setup-token 多账号池；固定为 `v6.10.9`（`785b00c3127eea6aa207f1207ead8a2aa93690a3`）。
- `third_party/grok2api`：Grok Build/Web/Console，多账号由它自己的管理页维护。
- `third_party/gemini-web2api`：Gemini Web；匿名 Flash 可直接使用，Pro 需要在私有运行配置中添加 Cookie。
- AtomCode2Api：现有独立容器，Lite2API 通过 `127.0.0.1:45678/v1` 接入。

### Docker Compose

初始化并按需启动：

```bash
./deploy/bootstrap-channels.sh
docker compose -f docker-compose.yml -f compose.channels.yml --profile gemini up -d --build
docker compose -f docker-compose.yml -f compose.channels.yml --profile grok up -d
docker compose -f docker-compose.yml -f compose.channels.yml --profile oauth up -d --build
```

端口只监听本机：Grok `45680`、Gemini `45681`、CLIProxyAPI `45682`。Compose 运行密钥、OAuth 文件和 Cookie 放在 `channels/runtime/`，该目录不会进入 Git。

### systemd 生产部署

不使用 Docker 的服务器可通过固定版本安装器部署 OAuth 适配器：

```bash
git submodule update --init third_party/cliproxyapi
sudo ./deploy/install-cliproxyapi-systemd.sh
sudo ./deploy/install-lite2api-systemd.sh
sudo ./deploy/server-ops/check-services.sh
```

安装器只接受子模块提交 `785b00c3127eea6aa207f1207ead8a2aa93690a3`，自动生成或复用两把独立密钥，将它们原子写入 Lite2API 环境文件，并验证管理鉴权和模型鉴权。systemd 部署使用以下安全边界：

- 服务：`cliproxyapi.service`
- 配置：`/etc/cliproxyapi/config.yaml`
- 仅服务端管理密钥：`/etc/cliproxyapi/cliproxyapi.env`
- OAuth 凭据：`/var/lib/cliproxyapi/auths/`
- 回环监听：`127.0.0.1:45682`

上述配置、环境文件和凭据目录禁止提交 Git。本轮生产更新按用户要求不创建备份，两个安装器也不会复制旧配置或凭据。

旧 Sub2API 账号导出可以按凭据类型一次拆分到三个安全边界：官方 API Key 进入 Lite2API，Grok OAuth 进入 Grok2API，其余受支持的 OAuth/setup-token 进入 CLIProxyAPI：

```bash
node deploy/migrate-sub2api-auths.mjs \
  --input /path/to/sub2api-export.json \
  --cliproxy-auth-dir channels/runtime/cliproxyapi/auths \
  --grok-output channels/runtime/cliproxyapi/grok-import.json \
  --lite-output channels/runtime/cliproxyapi/lite-import.json
```

脚本只输出分类数量，生成目录为 `0700`、凭据文件为 `0600`。它不会把 OAuth/Cookie 当作 API Key。生成文件仍需分别通过适配器/Lite2API 管理接口导入并验证，禁止提交 Git。

Grok 首次启动后访问 `http://127.0.0.1:45680`，使用 `.env` 中的 `GROK2API_ADMIN_PASSWORD` 登录、导入账号并创建 Client Key，再把 Client Key 写入 `.env` 的 `GROK2API_KEY`，重建 Lite2API 容器并在管理页启用 `grok-local`。

Gemini 默认使用临时会话、关闭请求日志、30 秒快速失败，并由随机内部 Key 保护。精简镜像刻意不安装可选 `httpx`：流式请求会先完整获取结果再输出标准 SSE，以避免 Gemini Web 长连接不结束导致并发槽悬挂。需要 Pro 或修复地区可用性时，只编辑 `channels/runtime/gemini-web2api/config.json` 中的 Cookie/代理配置，不要把凭据写进模板或 Git。

第三方渠道独立升级：

```bash
git submodule update --remote third_party/grok2api
git submodule update --remote third_party/gemini-web2api
```

CLIProxyAPI 生产版本必须显式修改子模块 revision 和 Compose 的 `VERSION`/`COMMIT`，不使用 `--remote` 自动升级。所有适配器升级后先在回环端口完成健康检查和真实请求，再切换 Lite2API 路由。不要自动跟随 `main` 部署。

## 快捷 OAuth 添加

生产环境为 CLIProxyAPI 设置独立的 `CLIPROXYAPI_MANAGEMENT_KEY`，同时注入 Lite2API 与 CLIProxyAPI。CLIProxyAPI 仍只监听 `127.0.0.1:45682`，管理面板保持关闭；Lite 管理页仅代理 Codex、Claude、Gemini CLI、Antigravity 和 Kimi 的授权链接、回调、状态与脱敏凭据列表。授权成功后，Compose 将凭据写入 `channels/runtime/cliproxyapi/auths/`，systemd 将其写入 `/var/lib/cliproxyapi/auths/`；浏览器无法读取管理密钥、Token 或凭据文件。

认证池和路由池是两个层次：一个 `cliproxy-oauth` 路由连接可复用多个 OAuth 凭据。成功登录后应在“渠道账号”看到新的脱敏凭据和统计，路由连接数量不增加是正常且节省资源的行为。

Claude 额度使用真实请求响应中的统一限额字段生成内存快照，支持 5 小时、7 天、Sonnet 周和 Opus 周窗口。Codex 使用 ChatGPT 官方 usage 接口读取主/次窗口，Gemini CLI 与 Antigravity 使用 Code Assist 官方 `retrieveUserQuota` 读取模型桶；它们仅在账号页可见时按需触发，每个凭据 10 分钟内最多一次并异步完成。反重力还展示可用 AI Credits，所有渠道发生 429 时都会保留模型冷却和重置时间。快照只包含脱敏后的窗口、百分比/余额、模型、重置和观测时间，服务重启后自然清空；没有可靠字段时保持 unknown，不用本地请求数推算官方余额。systemd 安装器会对固定上游提交幂等应用 `deploy/patches/cliproxyapi-quota-snapshot.patch`，不自动跟随第三方分支。

## 浏览器 Cookie / SSO 接入

- Gemini Web：在已登录 `gemini.google.com` 的浏览器中，用 Cookie-Editor 一类扩展导出 JSON、Netscape Cookie 或单行 Cookie；管理页只在浏览器本地归一化成 `name=value; ...`，然后手工写入 `channels/runtime/gemini-web2api/` 的私有配置。
- Grok Web/Console：优先使用 Grok2API 的 Build 设备 OAuth；已有 Web/Console 会话时，按 Grok2API 当前支持格式导出 SSO 文本、JSON 或 JSONL，再导入它自己的凭据存储。
- 两类敏感内容都不得提交 Lite2API API、配置文件或 Git。只有凭据已配置且需要承载流量时才启动对应 Compose profile；否则保持 `stopped`，避免常驻内存和探针开销。
