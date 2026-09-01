# 渠道适配层

Lite2API 核心不复制第三方项目的账号登录、Cookie 刷新或逆向协议代码。每个渠道作为独立进程暴露 OpenAI 兼容接口，核心只负责统一入口、密钥隔离、模型映射、并发、熔断、换号与统计。

每个账号同时声明 `adapter_id`、`instance_id` 和 `operations`。前两者用于稳定关联实现与本机实例，`operations` 用于在调度前按 Chat、Responses、Anthropic Messages、Embeddings、Images 或 Rerank 能力过滤。适配器“进程运行”不等于“可承载流量”：只有凭据、模型和 Lite2API 账号配置都就绪时状态才是 `ready`。

当前固定源码：

- `third_party/cliproxyapi`：Gemini CLI、Claude、OpenAI/Codex、Antigravity 的 OAuth/setup-token 多账号池；固定上游为 `v6.10.9`（`785b00c3127eea6aa207f1207ead8a2aa93690a3`），仓库维护构建为 `v6.10.9-lite2api.8`，四组补丁均校验 SHA-256。
- `third_party/grok2api`：Grok Build/Web/Console，多账号由它自己的管理页维护。
- `third_party/gemini-web2api`：Gemini Web；匿名 Flash 可直接使用，Pro 需要在私有运行配置中添加 Cookie。
- AtomCode2Api：现有独立容器，Lite2API 通过 `127.0.0.1:45678/v1` 接入。

### Docker Compose

初始化并按需启动：

```bash
git submodule sync --recursive
git submodule update --init --recursive third_party/gemini-web2api
./deploy/bootstrap-channels.sh
docker compose -f docker-compose.yml -f compose.channels.yml --profile gemini up -d --build
docker compose -f docker-compose.yml -f compose.channels.yml --profile grok up -d
docker compose -f docker-compose.yml -f compose.channels.yml --profile oauth up -d --build
```

Gemini profile 需要上面的固定子模块；Grok 使用 Compose 中固定的镜像 digest；OAuth 镜像会在构建阶段拉取并核验 CLIProxyAPI 固定提交，再按固定顺序应用四组维护补丁，因此不会使用现场 dirty 子模块。端口只发布到宿主回环：Grok `45680`、Gemini `45681`、CLIProxyAPI `45682`。每个渠道使用独立 bridge 网络以保留上游访问但隔离横向流量，并以非 root UID、只读根文件系统、空 capabilities 和资源上限运行。

`bootstrap-channels.sh` 会在锁内原子补齐 `.env` 的渠道密钥并生成 `0600` 私有配置；已存在配置作为迁移源，所以 Gemini Cookie 和其他手工字段不会因重跑被清空，脚本只同步托管密钥、容器监听地址与 CLIProxyAPI 路由策略。占位符、长度错误的托管密钥以及 runtime 符号链接会被拒绝。root 执行时使用 UID/GID `10001:10001` 并修正 owner；普通 Docker 用户执行时记录当前 UID/GID，Compose 使用同一身份，因此 `0600` 配置可读。若 `.env` 已显式配置另一 UID/GID，脚本会拒绝用不匹配的非 root 用户继续。Compose 运行密钥、OAuth 文件和 Cookie 放在 `channels/runtime/`，该目录和 `.env` 均不会进入 Docker build context 或 Git。

### systemd 生产部署

不使用 Docker 的服务器可通过固定版本安装器部署 OAuth 适配器：

```bash
git submodule update --init third_party/cliproxyapi
sudo ./deploy/install-lite2api-systemd.sh
sudo ./deploy/install-cliproxyapi-systemd.sh
sudo ./deploy/server-ops/check-services.sh
```

Lite2API 安装器会创建缺失的服务用户、目录、环境文件与初始配置。两个安装器都只从选定 Git 提交的 detached worktree 构建；dirty/untracked 文件不会进入产物。CLIProxyAPI 只接受子模块提交 `785b00c3127eea6aa207f1207ead8a2aa93690a3`，在隔离 worktree 中应用补丁，自动生成或复用两把独立密钥，并验证管理鉴权和模型鉴权。任一健康检查失败都会恢复升级前的文件和服务状态；root-only 回滚快照分别保存在 `/var/lib/lite2api-rollbacks/` 与 `/var/lib/cliproxyapi-rollbacks/`，不放在服务可写目录内。

- 服务：`cliproxyapi.service`
- 配置：`/etc/cliproxyapi/config.yaml`
- 仅服务端管理密钥：`/etc/cliproxyapi/cliproxyapi.env`
- OAuth 凭据：`/var/lib/cliproxyapi/auths/`
- 回环监听：`127.0.0.1:45682`

上述配置、环境文件和凭据目录禁止提交 Git。安装器回滚快照只用于本机短期升级恢复，不能替代加密异地备份。

旧 Sub2API 账号导出可以按凭据类型一次拆分到三个安全边界：官方 API Key 进入 Lite2API，Grok OAuth 进入 Grok2API，其余受支持的 OAuth/setup-token 进入 CLIProxyAPI：

```bash
node deploy/migrate-sub2api-auths.mjs \
  --input /path/to/sub2api-export.json \
  --cliproxy-auth-dir channels/runtime/cliproxyapi/auths \
  --grok-output channels/runtime/cliproxyapi/grok-import.json \
  --lite-output channels/runtime/cliproxyapi/lite-import.json
```

脚本先完整分类和校验，再把所有结果写入同文件系统 staging，并通过 `link(2)` 以 create-if-absent 语义一次提交；同邮箱账号带稳定摘要后缀，不会互相覆盖，任何碰撞都会回滚本次新增文件。默认遇到不支持账号时零写入失败；确认接受跳过时才可显式加 `--allow-unsupported`。输出永不覆盖既有文件，凭据继承 auth 目录 owner，目录为 `0700`、文件为 `0600`；需要指定容器身份时使用 `--owner UID:GID`。它不会把 OAuth/Cookie 当作 API Key。生成文件仍需分别通过适配器/Lite2API 管理接口导入并验证，禁止提交 Git。

Grok 首次启动后访问 `http://127.0.0.1:45680`，使用 `.env` 中的 `GROK2API_ADMIN_PASSWORD` 登录、导入账号并创建 Client Key，再把 Client Key 写入 `.env` 的 `GROK2API_KEY`，重建 Lite2API 容器并在管理页启用 `grok-local`。

Gemini 默认使用临时会话、关闭请求日志、30 秒快速失败，并由随机内部 Key 保护。精简镜像刻意不安装可选 `httpx`：流式请求会先完整获取结果再输出标准 SSE，以避免 Gemini Web 长连接不结束导致并发槽悬挂。需要 Pro 或修复地区可用性时，只编辑 `channels/runtime/gemini-web2api/config.json` 中的 Cookie/代理配置，不要把凭据写进模板或 Git。

第三方渠道独立升级：

```bash
git submodule update --remote third_party/grok2api
git submodule update --remote third_party/gemini-web2api
```

CLIProxyAPI 生产版本必须显式修改子模块 revision 和 Compose 的 `VERSION`/`COMMIT`，不使用 `--remote` 自动升级。所有适配器升级后先在回环端口完成健康检查和真实请求，再切换 Lite2API 路由。不要自动跟随 `main` 部署。

## 快捷 OAuth 添加

生产环境为 CLIProxyAPI 设置独立的 `CLIPROXYAPI_MANAGEMENT_KEY`，同时注入 Lite2API 与 CLIProxyAPI。CLIProxyAPI 仍只在宿主发布 `127.0.0.1:45682`，管理面板保持关闭；Lite 管理页仅代理 Codex、Claude、Gemini CLI、Antigravity 和 Kimi 的授权链接、回调、状态与脱敏凭据列表。临时 OAuth callback forwarder 在补丁中固定绑定 adapter 自身的 `127.0.0.1`，Compose 不发布这些回调端口；容器部署统一使用 Lite 管理页的“粘贴 localhost 回调”或设备码流程，不直接开启上游管理 Web UI。授权成功后，Compose 将凭据写入 `channels/runtime/cliproxyapi/auths/`，systemd 将其写入 `/var/lib/cliproxyapi/auths/`；浏览器无法读取管理密钥、Token 或凭据文件。

认证池和路由池是两个层次：一个 `cliproxy-oauth` 路由连接可复用多个 OAuth 凭据。成功登录后应在“渠道账号”看到新的脱敏凭据和统计，路由连接数量不增加是正常且节省资源的行为。认证账号优先级使用高值优先，先选最高的可用优先级层；同级可轮询或固定首选，禁用、鉴权失败、额度/模型冷却和上游错误会自动换号。Lite 外层显式路由默认严格按 `targets[]` 顺序，也可在“账号策略”中改为外层 priority 小值优先、最少负载、轮询或会话粘滞；两层不要用相反的 priority 方向互相推断。

仓库模板使用 `max-retry-credentials: 0`，让一次请求遍历所有当前可选凭据，并使用 `request-retry: 0` 在一轮失败后立即交给 Lite 外层目标链。选号策略通过回环管理 API 写回配置：Compose 只把单个 `config.yaml` 以可写 bind mount 暴露给非 root 容器；systemd 只放行 `/etc/cliproxyapi/config.yaml` 且保持 `root:cliproxyapi 0660`。安装器和渠道 bootstrap 重建配置时会保留现有 `round-robin` / `fill-first`，其余安全模板值仍由仓库收敛。

Claude 额度使用真实请求响应中的统一限额字段生成内存快照，支持 5 小时、7 天、Sonnet 周和 Opus 周窗口。Codex 使用 ChatGPT 官方 usage 接口读取主/次窗口，Gemini CLI 与 Antigravity 使用 Code Assist 官方 `retrieveUserQuota` 读取模型桶；它们仅在账号页可见时按需触发，每个凭据 10 分钟内最多一次并异步完成。反重力还展示可用 AI Credits，所有渠道发生 429 时都会保留模型冷却和重置时间。快照只包含脱敏后的窗口、百分比/余额、模型、重置和观测时间，服务重启后自然清空；没有可靠字段时保持 unknown，不用本地请求数推算官方余额。路由目标可选填公开的 `auth_index`，将请求固定到一个认证账号；CLIProxyAPI 会返回实际命中的公开索引，Lite2API 只记录脱敏索引并按“连接、认证账号、操作、模型”隔离熔断。空索引继续使用自动账号池。只有 CLIProxyAPI 明确证明请求尚未提交时，受信的内部标记才允许 5xx 跨账号重放；普通供应商 5xx 仍按结果不确定处理。Antigravity 的明确 `MODEL_CAPACITY_EXHAUSTED` 拒绝会归一为 429。systemd 安装器会对固定上游提交依次幂等应用 `deploy/patches/cliproxyapi-quota-snapshot.patch`、`deploy/patches/cliproxyapi-routing-reliability.patch`、`deploy/patches/cliproxyapi-auth-refresh.patch` 与 `deploy/patches/cliproxyapi-credential-routing.patch`，不自动跟随第三方分支。

## 浏览器 Cookie / SSO 接入

- Gemini Web：在已登录 `gemini.google.com` 的浏览器中，用 Cookie-Editor 一类扩展导出 JSON、Netscape Cookie 或单行 Cookie；管理页只在浏览器本地归一化成 `name=value; ...`，然后手工写入 `channels/runtime/gemini-web2api/` 的私有配置。
- Grok Web/Console：优先使用 Grok2API 的 Build 设备 OAuth；已有 Web/Console 会话时，按 Grok2API 当前支持格式导出 SSO 文本、JSON 或 JSONL，再导入它自己的凭据存储。
- 两类敏感内容都不得提交 Lite2API API、配置文件或 Git。只有凭据已配置且需要承载流量时才启动对应 Compose profile；否则保持 `stopped`，避免常驻内存和探针开销。
