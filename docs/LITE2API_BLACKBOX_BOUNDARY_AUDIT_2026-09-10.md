# Lite2API 黑盒出入口审计

审计时间：2026-09-10 UTC

## 0. 范围、口径、证据状态

- 黑盒主体是当前生产进程 `/usr/local/bin/lite2api`。Nginx、systemd、CLIProxyAPI、调用客户端、文件系统、进程环境均在黑盒之外。
- [推断] 运行中二进制报告版本 `deployed-20260901-e3927a69591a`；当前仓库 HEAD 为 `2f30cb4`，两者之间仅管理前端资源及其测试有差异，后端边界代码相同。依据：审计时执行二进制 `-version`、`git rev-parse HEAD`、`git diff --name-only e3927a6..2f30cb4` 的输出；该事实没有稳定的源文件行号。
- 当前生产入口为 `127.0.0.1:45679`，systemd 以 `/etc/lite2api/config.json` 启动进程：`/etc/systemd/system/lite2api.service:10`、`/etc/systemd/system/lite2api.service:13`，`/etc/lite2api/config.json:4`。
- 当前公网模型面由 Nginx `/lite/v1/` 改写后转发，管理面由 `/lite-admin/` 改写后转发，健康面仅公开 `/health`：`/etc/nginx/sites-available/sub2api.conf:46`、`/etc/nginx/sites-available/sub2api.conf:47`、`/etc/nginx/sites-available/sub2api.conf:48`、`/etc/nginx/sites-available/sub2api.conf:72`、`/etc/nginx/sites-available/sub2api.conf:79`、`/etc/nginx/sites-available/sub2api.conf:80`、`/etc/nginx/sites-available/sub2api.conf:93`、`/etc/nginx/sites-available/sub2api.conf:94`。
- 当前三个启用账号均指向回环 CLIProxyAPI `127.0.0.1:45682/v1`：`/etc/lite2api/config.json:40`、`/etc/lite2api/config.json:45`、`/etc/lite2api/config.json:324`、`/etc/lite2api/config.json:327`、`/etc/lite2api/config.json:332`、`/etc/lite2api/config.json:540`、`/etc/lite2api/config.json:543`、`/etc/lite2api/config.json:548`、`/etc/lite2api/config.json:665`。
- [推断] 对“谁发起公网请求”无法从服务端静态文件确定到某个具体客户端进程；下表只把 Lite2API 的直接 HTTP 对端写成 **Nginx worker 进程**，依据是上述生产反代配置。原始数据来源写为“远端 API 客户端”或“管理浏览器”，不虚构进程名。
- [推断] 只有 CLIProxyAPI 的鉴权 `/models` 适配器探针会在当前环境发出：审计时只检查环境项是否为空，得到 `CLIPROXYAPI_KEY=PRESENT`、`ATOMCODE2API_KEY=EMPTY`，另两个专用适配器 Key 未定义。代码在 Key 为空时会在发请求前返回：`/root/Lite2API/internal/gateway/adapters.go:307`、`/root/Lite2API/internal/gateway/adapters.go:312`、`/root/Lite2API/internal/gateway/adapters.go:315`。
- “真实”表示生产代码存在可达的边界调用，不表示审计时刻恰有流量。测试文件、`third_party/` 源码、构建脚本自身的 I/O 不算 Lite2API 进程边界。
- HTTP 请求的公共接入链是 `Run` 注册路由后由 `net/http` 调用处理函数：`/root/Lite2API/internal/gateway/server.go:18`、`/root/Lite2API/internal/gateway/server.go:27`、`/root/Lite2API/internal/gateway/server.go:28`、`/root/Lite2API/internal/gateway/server.go:35`、`/root/Lite2API/internal/gateway/server.go:48`、`/root/Lite2API/internal/gateway/server.go:53`。管理请求先经过 `ServeAdminAPI` 的网络、认证、CSRF 检查：`/root/Lite2API/internal/gateway/admin.go:19`、`/root/Lite2API/internal/gateway/admin.go:21`、`/root/Lite2API/internal/gateway/admin.go:38`、`/root/Lite2API/internal/gateway/admin.go:43`。
- 表内未写出的“具体数据”字段均为没有独立载荷的触发，不使用空值占位。

## 1. 真实函数外部调用入口

### 1.1 进程、文件、环境、信号层（12 条）

| ID | 传递者 / 数据来源 | 真实触发者 | 触发方式 | 频率 | 数据本身 | 单数接收者 / 接收方式 | 证据 |
|---|---|---|---|---|---|---|---|
| I-S01 | systemd 单元中的 `ExecStart` argv | `systemd` 服务管理器 | 进程启动 | 首次启动；失败后 3 秒重启 | `-config /etc/lite2api/config.json` | `main()`；通过 `flag.Parse()` 写入 `configPath` | `/etc/systemd/system/lite2api.service:13`、`/etc/systemd/system/lite2api.service:14`、`/etc/systemd/system/lite2api.service:15`；`/root/Lite2API/cmd/lite2api/main.go:16`、`/root/Lite2API/cmd/lite2api/main.go:17`、`/root/Lite2API/cmd/lite2api/main.go:20` |
| I-S02 | `/etc/lite2api/config.json` | `config.Load` 的调用方 | 文件读取 | 启动一次；SIGHUP 一次；管理变更一次；发现结果有变化时一次 | 服务、账号、能力、路由配置 | `config.Load(path string)`；参数 `path` 决定文件，JSON 返回值进入调用方 | `/root/Lite2API/internal/config/config.go:168`、`/root/Lite2API/internal/config/config.go:169`；`/root/Lite2API/internal/gateway/gateway.go:83`、`/root/Lite2API/internal/gateway/gateway.go:131`、`/root/Lite2API/internal/gateway/admin.go:371` |
| I-S03 | Lite2API 进程环境 | `applyEnvironmentOverrides` | 环境变量读取 | 每次 `config.Normalize` | 管理自动登录、CIDR、资源上限、日志保留覆盖值 | `applyEnvironmentOverrides(cfg *Config)`；写入参数 `cfg` | `/root/Lite2API/internal/config/config.go:378`、`/root/Lite2API/internal/config/config.go:379`、`/root/Lite2API/internal/config/config.go:382`、`/root/Lite2API/internal/config/config.go:388`、`/root/Lite2API/internal/config/config.go:394` |
| I-S04 | `LITE2API_API_KEYS` 指定的进程环境项 | `Config.GatewayKeys` | 环境变量读取 | 启动、重载、配置提交校验 | 旧式客户端 Bearer Key 列表 | `GatewayKeys()`；通过返回值接收 | `/root/Lite2API/internal/config/config.go:821`、`/root/Lite2API/internal/config/config.go:823`、`/root/Lite2API/internal/config/config.go:830` |
| I-S05 | `LITE2API_ADMIN_TOKEN` 指定的进程环境项 | `Config.ResolvedAdminToken` | 环境变量读取 | 启动、重载、配置提交校验 | 管理 Token | `ResolvedAdminToken()`；通过返回值接收 | `/root/Lite2API/internal/config/config.go:833`、`/root/Lite2API/internal/config/config.go:835`、`/root/Lite2API/internal/config/config.go:839` |
| I-S06 | 各账号 `api_key_env` 指定的进程环境项 | `Account.ResolvedAPIKey` | 环境变量读取 | 构建运行态；调用发现或探针时 | 上游认证密钥 | `ResolvedAPIKey()`；通过返回值接收 | `/root/Lite2API/internal/config/config.go:842`、`/root/Lite2API/internal/config/config.go:844`、`/root/Lite2API/internal/config/config.go:848` |
| I-S07 | 各账号 `headers_env` 指定的进程环境项 | `Account.ResolvedHeaders` | 环境变量读取 | 构建运行态；调用发现或探针时 | 上游自定义 Header 值 | `ResolvedHeaders()`；通过返回值接收 | `/root/Lite2API/internal/config/config.go:851`、`/root/Lite2API/internal/config/config.go:856`、`/root/Lite2API/internal/config/config.go:859` |
| I-S08 | `CLIPROXYAPI_MANAGEMENT_KEY` | 管理面适配器调用 | 环境变量读取 | 每次 CLIProxyAPI 管理请求 | CLIProxyAPI 管理 Bearer Key | `callOAuthAdapter(..., target any)`；通过局部变量 `key` 接收 | `/root/Lite2API/internal/gateway/oauthadapter.go:591`、`/root/Lite2API/internal/gateway/oauthadapter.go:596`、`/root/Lite2API/internal/gateway/oauthadapter.go:615` |
| I-S09 | `CLIPROXYAPI_MANAGEMENT_URL` | 管理面适配器调用 | 环境变量读取 | 每次 CLIProxyAPI 管理请求 | CLIProxyAPI 管理基址；空值退回 `127.0.0.1:45682` | `oauthAdapterBaseURL()`；通过局部变量 `raw` 接收 | `/root/Lite2API/internal/gateway/oauthadapter.go:24`、`/root/Lite2API/internal/gateway/oauthadapter.go:648`、`/root/Lite2API/internal/gateway/oauthadapter.go:649`、`/root/Lite2API/internal/gateway/oauthadapter.go:651` |
| I-S10 | `/etc/lite2api/client_keys.json` | `Gateway.New` | 文件读取 | 进程启动一次 | 托管客户端 Key 摘要、名称、策略、期限 | `ClientKeyStore.load()`；通过接收者字段 `s.path` 定位，无显式路径参数 | `/root/Lite2API/internal/gateway/gateway.go:114`、`/root/Lite2API/internal/gateway/gateway.go:115`；`/root/Lite2API/internal/gateway/clientkeys.go:119`、`/root/Lite2API/internal/gateway/clientkeys.go:138`、`/root/Lite2API/internal/gateway/clientkeys.go:139` |
| I-S11 | `/etc/lite2api/request.log` 及轮转备份 | `Gateway.New` | 文件读取 | 进程启动一次 | 最近请求摘要、每路由最近观测 | `loadRequestState(path, backups, maxRecords, routeFingerprints)`；参数 `path` 等 | `/root/Lite2API/internal/gateway/gateway.go:91`、`/root/Lite2API/internal/gateway/gateway.go:92`、`/root/Lite2API/internal/gateway/gateway.go:107`；`/root/Lite2API/internal/gateway/requestlog.go:329`、`/root/Lite2API/internal/gateway/requestlog.go:341` |
| I-S12 | Linux 进程信号队列 | `systemd` 或有权限的运维进程 | POSIX signal | 事件驱动 | `SIGHUP` 重载；`SIGINT`、`SIGTERM` 停机 | `Gateway.Run(ctx)`；通过局部 channel `signals` 隐式接收 | `/root/Lite2API/internal/gateway/server.go:18`、`/root/Lite2API/internal/gateway/server.go:60`、`/root/Lite2API/internal/gateway/server.go:61`、`/root/Lite2API/internal/gateway/server.go:72`、`/root/Lite2API/internal/gateway/server.go:73`、`/root/Lite2API/internal/gateway/server.go:81` |

### 1.2 公共状态层（6 条）

频率均为事件驱动、每次请求一次。公网暴露项的直接触发者是 Nginx worker；仅回环注册项的具体调用进程无法由监听端静态确定，以下按仓库内可定位的运维调用方标成推断。

| ID | 来源数据 | 真实触发者 | 输入数据 | 单数接收者 / 参数 | 证据 |
|---|---|---|---|---|---|
| I-P01 | 远端健康客户端的 `GET /health` | Nginx worker 进程 | 请求 Header、来源地址 | `serveHealth(w, r)`；`r` | `/etc/nginx/sites-available/sub2api.conf:93`、`/etc/nginx/sites-available/sub2api.conf:94`；`/root/Lite2API/internal/gateway/server.go:28`、`/root/Lite2API/internal/gateway/server.go:174` |
| I-P02 | 回环客户端的 `GET /health/details` | [推断] 运维人员启动的 `/usr/bin/curl`；依据同类健康检查脚本 | 请求 Header、来源地址 | `serveHealth(w, r)`；`r` | `/root/Lite2API/internal/gateway/server.go:29`、`/root/Lite2API/internal/gateway/server.go:174`；`/root/Lite2API/deploy/server-ops/check-services.sh:102` |
| I-P03 | 回环存活探针的 `GET /livez` | [推断] `check-services.sh` 启动的 `/usr/bin/curl`；依据脚本调用 | 请求 Header、来源地址 | `serveLiveness(w, r)`；`r` | `/root/Lite2API/internal/gateway/server.go:30`、`/root/Lite2API/internal/gateway/server.go:86`；`/root/Lite2API/deploy/server-ops/check-services.sh:102` |
| I-P04 | 回环就绪探针的 `GET /readyz` | [推断] `check-services.sh` 启动的 `/usr/bin/curl`；依据脚本调用 | 请求 Header、来源地址 | `serveReadiness(w, r)`；`r` | `/root/Lite2API/internal/gateway/server.go:31`、`/root/Lite2API/internal/gateway/server.go:90`；`/root/Lite2API/deploy/server-ops/check-services.sh:105` |
| I-P05 | 管理浏览器的 `GET /admin` 或 `/admin/` | Nginx worker 进程 | `Accept-Encoding`、来源地址 | `serveAdminPage(w, r)`；`r` | `/etc/nginx/sites-available/sub2api.conf:72`、`/etc/nginx/sites-available/sub2api.conf:79`、`/etc/nginx/sites-available/sub2api.conf:80`；`/root/Lite2API/internal/gateway/server.go:33`、`/root/Lite2API/internal/gateway/server.go:34`、`/root/Lite2API/internal/gateway/server.go:192`、`/root/Lite2API/internal/gateway/server.go:205` |
| I-P06 | 回环 HTTP 客户端的 `GET /` | [推断] 运维人员启动的 HTTP 客户端；公网根路径由 Nginx 自行 308，不到达 Lite2API | 请求路径、来源地址 | `Run` 内根路径闭包；`r` | `/etc/nginx/sites-available/sub2api.conf:101`、`/etc/nginx/sites-available/sub2api.conf:102`；`/root/Lite2API/internal/gateway/server.go:36`、`/root/Lite2API/internal/gateway/server.go:37`、`/root/Lite2API/internal/gateway/server.go:42`、`/root/Lite2API/internal/gateway/server.go:46` |

### 1.3 模型协议层（8 条）

数据来源是远端 API 客户端，直接触发者是生产 Nginx worker；触发方式均为 HTTP 转发，频率均为事件驱动、每次客户端请求一次。所有条目首先进入 `ServeGateway(w, r)`；POST 条目的最终核心消费者是 `doUpstream(..., inbound *http.Request, body []byte, ...)`，不是把它藏在链路末尾：`/root/Lite2API/internal/gateway/gateway.go:358`、`/root/Lite2API/internal/gateway/gateway.go:429`、`/root/Lite2API/internal/gateway/gateway.go:625`、`/root/Lite2API/internal/gateway/gateway.go:649`、`/root/Lite2API/internal/gateway/gateway.go:986`。

| ID | HTTP 输入 | 业务数据 | 具体真实数据 | 单数接收者 / 输入参数 | 证据 |
|---|---|---|---|---|---|
| I-G01 | `GET /v1/models` | Key 可见模型目录请求 | Bearer Key | `serveModels(w, state, lease)`；`lease` | `/root/Lite2API/internal/gateway/gateway.go:380`、`/root/Lite2API/internal/gateway/gateway.go:386`、`/root/Lite2API/internal/gateway/gateway.go:1115` |
| I-G02 | `POST /v1/chat/completions` | OpenAI Chat 请求 | JSON `model`、`messages`、可选 `stream` | `doUpstream(..., inbound, body, ...)`；`inbound`、`body` | `/root/Lite2API/internal/gateway/gateway.go:1127`、`/root/Lite2API/internal/gateway/gateway.go:1129` |
| I-G03 | `POST /v1/responses` | OpenAI Responses 请求 | JSON `model`、`input`、可选 `stream` | `doUpstream(..., inbound, body, ...)`；`inbound`、`body` | `/root/Lite2API/internal/gateway/gateway.go:1131` |
| I-G04 | `POST /v1/messages` | Anthropic Messages 请求 | JSON `model`、`messages`、可选 `stream` | `doUpstream(..., inbound, body, ...)`；`inbound`、`body` | `/root/Lite2API/internal/gateway/gateway.go:1133` |
| I-G05 | `POST /v1/embeddings` | Embedding 请求 | JSON `model`、`input` | `doUpstream(..., inbound, body, ...)`；`inbound`、`body` | `/root/Lite2API/internal/gateway/gateway.go:1135` |
| I-G06 | `POST /v1/images/generations` | 图像生成请求 | JSON `model`、提示字段、图像参数 | `doUpstream(..., inbound, body, ...)`；`inbound`、`body` | `/root/Lite2API/internal/gateway/gateway.go:1137` |
| I-G07 | `POST /v1/rerank` | 重排请求 | JSON `model`、query、documents | `doUpstream(..., inbound, body, ...)`；`inbound`、`body` | `/root/Lite2API/internal/gateway/gateway.go:1139` |
| I-G08 | `/v1/*` 非法方法或未支持路径 | 模型协议拒绝请求 | HTTP method、path、Bearer Key | `ServeGateway(w, r)`；`r.Method`、`r.URL.Path` | `/root/Lite2API/internal/gateway/gateway.go:358`、`/root/Lite2API/internal/gateway/gateway.go:380`、`/root/Lite2API/internal/gateway/gateway.go:390`、`/root/Lite2API/internal/gateway/gateway.go:395`、`/root/Lite2API/internal/gateway/gateway.go:397` |

### 1.4 管理 HTTP 层（29 条）

数据来源是管理浏览器或显式管理 API 调用方；直接触发者是生产 Nginx worker；触发方式均为 HTTP 转发，频率均为事件驱动、每次管理操作一次。所有请求先由 `ServeAdminAPI` 分派，表中只保留最终单数接收者。生产管理前缀、来源 allowlist、转发位置见 `/etc/nginx/sites-available/sub2api.conf:72`、`/etc/nginx/sites-available/sub2api.conf:73`、`/etc/nginx/sites-available/sub2api.conf:77`、`/etc/nginx/sites-available/sub2api.conf:79`、`/etc/nginx/sites-available/sub2api.conf:80`。

| ID | 方法及路径 | 业务数据 / 具体输入 | 单数接收者 / 输入参数 | 证据 |
|---|---|---|---|---|
| I-A01 | `POST /admin/api/login` | JSON `token`；或显式自动登录空 token | `serveAdminLogin(w, r, state)`；`r.Body` | `/root/Lite2API/internal/gateway/admin.go:34`、`/root/Lite2API/internal/gateway/admin.go:35`、`/root/Lite2API/internal/gateway/admin.go:231`、`/root/Lite2API/internal/gateway/admin.go:232` |
| I-A02 | `GET /admin/api/session` | 管理 Cookie 或 Bearer Token | `ServeAdminAPI(w, r)`；通过 `r` 隐式认证 | `/root/Lite2API/internal/gateway/admin.go:38`、`/root/Lite2API/internal/gateway/admin.go:48` |
| I-A03 | `POST /admin/api/logout` | 管理 Cookie、CSRF | `ServeAdminAPI(w, r)`；`r` | `/root/Lite2API/internal/gateway/admin.go:50`、`/root/Lite2API/internal/gateway/admin.go:51` |
| I-A04 | `GET /admin/api/state` | 管理凭据 | `ServeAdminAPI(w, r)`；`r` | `/root/Lite2API/internal/gateway/admin.go:54`、`/root/Lite2API/internal/gateway/admin.go:55` |
| I-A05 | `GET /admin/api/trends?range=...` | `range`: `1h/6h/24h/3d/7d/all` | `ServeAdminAPI(w, r)`；`r.URL.Query()` | `/root/Lite2API/internal/gateway/admin.go:57`、`/root/Lite2API/internal/gateway/admin.go:58`、`/root/Lite2API/internal/gateway/admin.go:211` |
| I-A06 | `GET /admin/api/adapters` | 管理凭据 | `Gateway.AdapterCatalog(ctx, accounts)`；`ctx`、`accounts` | `/root/Lite2API/internal/gateway/admin.go:64`、`/root/Lite2API/internal/gateway/admin.go:65`；`/root/Lite2API/internal/gateway/adapters.go:161` |
| I-A07 | `POST /admin/api/prompt-test` | `account_id`、`model`、`messages`、采样参数 | `servePromptTest(w, r, state)`；`r.Body` | `/root/Lite2API/internal/gateway/admin.go:66`、`/root/Lite2API/internal/gateway/admin.go:67`；`/root/Lite2API/internal/gateway/prompttest.go:28`、`/root/Lite2API/internal/gateway/prompttest.go:36` |
| I-A08 | `GET /admin/api/client-keys` | 管理凭据 | `ClientKeyStore.List()`；接收者快照字段 | `/root/Lite2API/internal/gateway/admin.go:68`、`/root/Lite2API/internal/gateway/admin.go:69`；`/root/Lite2API/internal/gateway/clientkeys.go:386` |
| I-A09 | `POST /admin/api/client-keys` | `name`、模型白名单、RPM、并发上限、过期时间 | `ClientKeyStore.Create(input ClientKeyCreate)`；`input` | `/root/Lite2API/internal/gateway/admin.go:70`、`/root/Lite2API/internal/gateway/admin.go:71`、`/root/Lite2API/internal/gateway/admin.go:75`；`/root/Lite2API/internal/gateway/clientkeys.go:273` |
| I-A10 | `PUT /admin/api/client-keys/:id` | 路径 `id`、更新 JSON | `ClientKeyStore.Update(id, input)`；`id`、`input` | `/root/Lite2API/internal/gateway/admin.go:81`、`/root/Lite2API/internal/gateway/admin.go:82`、`/root/Lite2API/internal/gateway/admin.go:87`；`/root/Lite2API/internal/gateway/clientkeys.go:320` |
| I-A11 | `DELETE /admin/api/client-keys/:id` | 路径 `id` | `ClientKeyStore.Delete(id)`；`id` | `/root/Lite2API/internal/gateway/admin.go:93`、`/root/Lite2API/internal/gateway/admin.go:94`、`/root/Lite2API/internal/gateway/admin.go:99`；`/root/Lite2API/internal/gateway/clientkeys.go:370` |
| I-A12 | `POST /admin/api/reload` | 管理凭据、CSRF | `Gateway.Reload()`；无显式数据参数，通过 `g.configPath` 读配置 | `/root/Lite2API/internal/gateway/admin.go:104`、`/root/Lite2API/internal/gateway/admin.go:105`；`/root/Lite2API/internal/gateway/gateway.go:125`、`/root/Lite2API/internal/gateway/gateway.go:131` |
| I-A13 | `POST /admin/api/oauth/start` | `provider`、可选 `project_id` | `serveOAuthStart(w, r)`；`r.Body` | `/root/Lite2API/internal/gateway/admin.go:110`、`/root/Lite2API/internal/gateway/admin.go:111`；`/root/Lite2API/internal/gateway/oauthadapter.go:41`、`/root/Lite2API/internal/gateway/oauthadapter.go:169` |
| I-A14 | `POST /admin/api/oauth/callback` | `provider`、`redirect_url`、`state` | `serveOAuthCallback(w, r)`；`r.Body` | `/root/Lite2API/internal/gateway/admin.go:112`、`/root/Lite2API/internal/gateway/admin.go:113`；`/root/Lite2API/internal/gateway/oauthadapter.go:46`、`/root/Lite2API/internal/gateway/oauthadapter.go:199` |
| I-A15 | `POST /admin/api/oauth/status` | `state`、可选 `provider` | `serveOAuthStatus(w, r)`；`r.Body` | `/root/Lite2API/internal/gateway/admin.go:114`、`/root/Lite2API/internal/gateway/admin.go:115`；`/root/Lite2API/internal/gateway/oauthadapter.go:52`、`/root/Lite2API/internal/gateway/oauthadapter.go:225` |
| I-A16 | `GET /admin/api/oauth/accounts` | 管理凭据 | `serveOAuthAccounts(w, r)`；`r.Context()` | `/root/Lite2API/internal/gateway/admin.go:116`、`/root/Lite2API/internal/gateway/admin.go:117`；`/root/Lite2API/internal/gateway/oauthadapter.go:264` |
| I-A17 | `POST /admin/api/oauth/accounts/refresh` | 可选 `id`；`all` | `serveOAuthAccountRefresh(w, r)`；`r.Body` | `/root/Lite2API/internal/gateway/admin.go:118`、`/root/Lite2API/internal/gateway/admin.go:119`；`/root/Lite2API/internal/gateway/oauthadapter.go:66`、`/root/Lite2API/internal/gateway/oauthadapter.go:364` |
| I-A18 | `POST /admin/api/oauth/accounts/priority` | `id`、`priority` | `serveOAuthAccountPriority(w, r)`；`r.Body` | `/root/Lite2API/internal/gateway/admin.go:120`、`/root/Lite2API/internal/gateway/admin.go:121`；`/root/Lite2API/internal/gateway/oauthadapter.go:71`、`/root/Lite2API/internal/gateway/oauthadapter.go:273` |
| I-A19 | `DELETE /admin/api/oauth/accounts` | JSON `id` | `serveOAuthAccountDelete(w, r)`；`r.Body` | `/root/Lite2API/internal/gateway/admin.go:122`、`/root/Lite2API/internal/gateway/admin.go:123`；`/root/Lite2API/internal/gateway/oauthadapter.go:62`、`/root/Lite2API/internal/gateway/oauthadapter.go:397` |
| I-A20 | `POST /admin/api/oauth/accounts/status` | `id`、`disabled` | `serveOAuthAccountStatus(w, r)`；`r.Body` | `/root/Lite2API/internal/gateway/admin.go:124`、`/root/Lite2API/internal/gateway/admin.go:125`；`/root/Lite2API/internal/gateway/oauthadapter.go:57`、`/root/Lite2API/internal/gateway/oauthadapter.go:416` |
| I-A21 | `GET /admin/api/oauth/routing` | 管理凭据 | `serveOAuthRouting(w, r)`；`r.Context()` | `/root/Lite2API/internal/gateway/admin.go:126`、`/root/Lite2API/internal/gateway/admin.go:127`；`/root/Lite2API/internal/gateway/oauthadapter.go:304` |
| I-A22 | `PUT /admin/api/oauth/routing` | JSON `strategy` | `serveOAuthRoutingUpdate(w, r)`；`r.Body` | `/root/Lite2API/internal/gateway/admin.go:128`、`/root/Lite2API/internal/gateway/admin.go:129`；`/root/Lite2API/internal/gateway/oauthadapter.go:76`、`/root/Lite2API/internal/gateway/oauthadapter.go:320` |
| I-A23 | `POST /admin/api/accounts/test` | JSON `account`，含连接、认证、模型配置 | `serveAccountTest(w, r, state)`；`r.Body` | `/root/Lite2API/internal/gateway/admin.go:130`、`/root/Lite2API/internal/gateway/admin.go:131`；`/root/Lite2API/internal/gateway/accounttest.go:23`、`/root/Lite2API/internal/gateway/accounttest.go:38` |
| I-A24 | `POST /admin/api/accounts/export` | `ids`、`include_proxies` | `ExportAccounts(cfg, request)`；`request` | `/root/Lite2API/internal/gateway/admin.go:132`、`/root/Lite2API/internal/gateway/admin.go:133`、`/root/Lite2API/internal/gateway/admin.go:137`；`/root/Lite2API/internal/gateway/accountexport.go:15`、`/root/Lite2API/internal/gateway/accountexport.go:20` |
| I-A25 | `POST /admin/api/accounts/import` | `data`、`mode`、`dry_run` | `Gateway.ImportAccounts(ctx, request)`；`request` | `/root/Lite2API/internal/gateway/admin.go:143`、`/root/Lite2API/internal/gateway/admin.go:144`、`/root/Lite2API/internal/gateway/admin.go:148`；`/root/Lite2API/internal/gateway/accountimport.go:107` |
| I-A26 | `PUT /admin/api/accounts` | JSON `config.Account` | `Gateway.UpsertAccount(account)`；`account` | `/root/Lite2API/internal/gateway/admin.go:158`、`/root/Lite2API/internal/gateway/admin.go:159`、`/root/Lite2API/internal/gateway/admin.go:163`、`/root/Lite2API/internal/gateway/admin.go:283` |
| I-A27 | `DELETE /admin/api/accounts/:id` | 路径 `id` | `Gateway.DeleteAccount(id)`；`id` | `/root/Lite2API/internal/gateway/admin.go:168`、`/root/Lite2API/internal/gateway/admin.go:169`、`/root/Lite2API/internal/gateway/admin.go:174`、`/root/Lite2API/internal/gateway/admin.go:312` |
| I-A28 | `PUT /admin/api/routes` | JSON `map[string]config.Route` | `Gateway.ReplaceRoutes(routes)`；`routes` | `/root/Lite2API/internal/gateway/admin.go:179`、`/root/Lite2API/internal/gateway/admin.go:180`、`/root/Lite2API/internal/gateway/admin.go:184`、`/root/Lite2API/internal/gateway/admin.go:353` |
| I-A29 | `/admin/api/*` 非法方法或未支持路径 | 管理协议拒绝请求 | HTTP method、path、管理凭据、可选 CSRF | `ServeAdminAPI(w, r)`；`r.Method`、局部 `path` | `/root/Lite2API/internal/gateway/admin.go:19`、`/root/Lite2API/internal/gateway/admin.go:33`、`/root/Lite2API/internal/gateway/admin.go:47`、`/root/Lite2API/internal/gateway/admin.go:189`、`/root/Lite2API/internal/gateway/admin.go:190` |

### 1.5 外部服务返回层（8 条）

| ID | 传递者 / 数据来源 | 真实触发者 | 触发方式 | 频率 | 数据本身 | 单数接收者 / 参数 | 证据 |
|---|---|---|---|---|---|---|---|
| I-R01 | 当前 CLIProxyAPI `127.0.0.1:45682` | `ServeGateway` 经 `doUpstream` | HTTP 响应读取 | 每次模型上游尝试 | 状态、Header、流式或非流式正文 | `ServeGateway(w, r)`；通过 `resp *http.Response` 接收并流向客户端 | `/root/Lite2API/internal/gateway/gateway.go:649`、`/root/Lite2API/internal/gateway/gateway.go:698`、`/root/Lite2API/internal/gateway/gateway.go:701`、`/root/Lite2API/internal/gateway/gateway.go:758`、`/root/Lite2API/internal/gateway/gateway.go:769` |
| I-R02 | 当前 CLIProxyAPI `127.0.0.1:45682` | `servePromptTest` 经 `doUpstream` | HTTP 响应读取 | 每次提示测试 | 状态、JSON 正文 | `servePromptTest(w, r, state)`；局部 `resp`、`responseBody` | `/root/Lite2API/internal/gateway/prompttest.go:147`、`/root/Lite2API/internal/gateway/prompttest.go:152`、`/root/Lite2API/internal/gateway/prompttest.go:153`、`/root/Lite2API/internal/gateway/prompttest.go:179` |
| I-R03 | 被测试账号的 `/models` 服务 | `serveAccountTest` 经 `probeAccountModels` | HTTP 响应读取 | 每次账号测试 | HTTP 状态、模型目录、Content-Type、延迟 | `probeAccountModels(parent, account)`；局部 `resp`、`body` | `/root/Lite2API/internal/gateway/accounttest.go:77`、`/root/Lite2API/internal/gateway/accounttest.go:145`、`/root/Lite2API/internal/gateway/accounttest.go:154`、`/root/Lite2API/internal/gateway/accounttest.go:162` |
| I-R04 | 当前 CLIProxyAPI `/v1/models` | `runCapabilityDiscovery` | HTTP 响应读取 | 启动后 2 秒；随后每 10 分钟、每启用账号一次 | 模型 ID、推理档位、服务层级 | `fetchDiscoveredCatalog(ctx, client, account, rich)`；局部 `resp`、`data` | `/root/Lite2API/internal/gateway/capabilitysync.go:19`、`/root/Lite2API/internal/gateway/capabilitysync.go:36`、`/root/Lite2API/internal/gateway/capabilitysync.go:37`、`/root/Lite2API/internal/gateway/capabilitysync.go:47`、`/root/Lite2API/internal/gateway/capabilitysync.go:314`、`/root/Lite2API/internal/gateway/capabilitysync.go:349`、`/root/Lite2API/internal/gateway/capabilitysync.go:357` |
| I-R05 | 当前 CLIProxyAPI 根端点 | `GET /admin/api/adapters` 经适配器探针 | HTTP 响应读取 | 管理读取触发；成功缓存 60 秒，失败最多 5 秒 | 就绪状态、延迟 | `adapterProbeCache.probeNow(ctx, item, credential, now)`；局部 `resp` | `/root/Lite2API/internal/gateway/adapters.go:161`、`/root/Lite2API/internal/gateway/adapters.go:211`、`/root/Lite2API/internal/gateway/adapters.go:244`、`/root/Lite2API/internal/gateway/adapters.go:265`、`/root/Lite2API/internal/gateway/adapters.go:277` |
| I-R06 | 当前 CLIProxyAPI `/v1/models` | `GET /admin/api/adapters` 经模型探针 | HTTP 响应读取 | 管理读取触发；沿用同一探针缓存 | 模型目录、认证状态 | `adapterProbeCache.adapterModels(ctx, item, credential)`；局部 `resp`、`data` | `/root/Lite2API/internal/gateway/adapters.go:293`、`/root/Lite2API/internal/gateway/adapters.go:307`、`/root/Lite2API/internal/gateway/adapters.go:315`、`/root/Lite2API/internal/gateway/adapters.go:320`、`/root/Lite2API/internal/gateway/adapters.go:334` |
| I-R07 | 当前 CLIProxyAPI 管理 API | OAuth 管理处理函数经 `callOAuthAdapter` | HTTP 响应读取 | 每次对应管理操作 | 授权会话、状态、凭据摘要、额度、路由策略、刷新结果 | `callOAuthAdapter(..., target any)`；通过单个参数 `target` 解码 | `/root/Lite2API/internal/gateway/oauthadapter.go:591`、`/root/Lite2API/internal/gateway/oauthadapter.go:620`、`/root/Lite2API/internal/gateway/oauthadapter.go:625`、`/root/Lite2API/internal/gateway/oauthadapter.go:642` |
| I-R08 | 当前 CLIProxyAPI `/v1/models` | `ensureOAuthPoolAccount` 经 `discoverOAuthModels` | HTTP 响应读取 | OAuth 成功后按需一次 | 模型目录 | `discoverOAuthModels(ctx)`；通过局部 `resp` 和 `payload` 接收 | `/root/Lite2API/internal/gateway/oauthadapter.go:713`、`/root/Lite2API/internal/gateway/oauthadapter.go:749`、`/root/Lite2API/internal/gateway/oauthadapter.go:761`、`/root/Lite2API/internal/gateway/oauthadapter.go:769`、`/root/Lite2API/internal/gateway/oauthadapter.go:774` |

### 1.6 CLI 诊断层（1 条）

| ID | 传递者 / 数据来源 | 真实触发者 | 触发方式 | 频率 | 数据本身 | 单数接收者 / 参数 | 证据 |
|---|---|---|---|---|---|---|---|
| I-C01 | 调用 shell 传入的 argv | 运维人员启动的 shell 进程 | 进程启动 | 每次诊断命令一次 | `-version`；或当前部署的 `-check-config -config /etc/lite2api/config.json` | `main()`；通过 `showVersion`、`checkConfig`、`configPath` 接收 | `/root/Lite2API/cmd/lite2api/main.go:16`、`/root/Lite2API/cmd/lite2api/main.go:17`、`/root/Lite2API/cmd/lite2api/main.go:18`、`/root/Lite2API/cmd/lite2api/main.go:19`、`/root/Lite2API/cmd/lite2api/main.go:20` |

### 1.7 二级系统输入层（11 条）

这些条目与 1.1 的数据源可能相同，但真实接收函数或触发上下文不同，所以按粒度律拆开。

| ID | 传递者 / 数据来源 | 真实触发者 | 触发方式 | 频率 | 数据本身 | 单数接收者 / 接收方式 | 证据 |
|---|---|---|---|---|---|---|---|
| I-S13 | `/etc/lite2api/config.json` | 控制面配置提交 | 文件读取 | 每次实际配置落盘前一次 | 未叠加环境覆盖的期望配置 | `Store.SaveEffective(cfg Config)`；通过局部 `desired` 接收 | `/root/Lite2API/internal/config/config.go:888`、`/root/Lite2API/internal/config/config.go:892`、`/root/Lite2API/internal/config/config.go:893`、`/root/Lite2API/internal/config/config.go:895` |
| I-S14 | 当前请求日志文件元数据 | `newRequestLogWriter` 或 reconfigure | 文件 `stat` | writer 打开时一次 | 是否存在、当前字节数 | `requestLogWriter.open()`；通过局部 `info` 接收 | `/root/Lite2API/internal/gateway/requestlog.go:101`、`/root/Lite2API/internal/gateway/requestlog.go:108`、`/root/Lite2API/internal/gateway/requestlog.go:109`、`/root/Lite2API/internal/gateway/requestlog.go:121`、`/root/Lite2API/internal/gateway/requestlog.go:127` |
| I-S15 | 请求日志备份文件元数据 | `requestLogWriter.write` 触发轮转 | 文件 `stat` | 达到大小上限时，每个备份候选一次 | 备份是否存在 | `requestLogWriter.rotate()`；通过 `os.Stat(source)` 结果接收 | `/root/Lite2API/internal/gateway/requestlog.go:221`、`/root/Lite2API/internal/gateway/requestlog.go:234`、`/root/Lite2API/internal/gateway/requestlog.go:249` |
| I-S16 | 专用适配器 Key 环境项 | `GET /admin/api/adapters` | 环境变量读取 | 探针缓存未命中时，每个已安装适配器一次 | 探针凭据；摘要参与缓存身份 | `adapterProbeIdentity(item)`；通过返回值 `credential` 接收 | `/root/Lite2API/internal/gateway/adapters.go:208`、`/root/Lite2API/internal/gateway/adapters.go:209`、`/root/Lite2API/internal/gateway/adapters.go:230`、`/root/Lite2API/internal/gateway/adapters.go:232`、`/root/Lite2API/internal/gateway/adapters.go:233` |
| I-S17 | `CLIPROXYAPI_KEY` | OAuth 成功后的账号池检查 | 环境变量读取 | 每次需要新建共享池账号 | 服务认证 Key 是否存在 | `ensureOAuthPoolAccount(ctx, provider)`；通过条件判断直接接收 | `/root/Lite2API/internal/gateway/oauthadapter.go:704`、`/root/Lite2API/internal/gateway/oauthadapter.go:727` |
| I-S18 | `CLIPROXYAPI_KEY` | OAuth 模型发现 | 环境变量读取 | 每次 OAuth 模型发现一次 | `/v1/models` Bearer Key | `discoverOAuthModels(ctx)`；写入请求 Authorization Header | `/root/Lite2API/internal/gateway/oauthadapter.go:749`、`/root/Lite2API/internal/gateway/oauthadapter.go:760` |
| I-S19 | 服务覆盖环境项 | 控制面配置提交 | 环境变量读取 | 每次实际配置落盘前 | 哪些服务字段归环境所有 | `preserveEnvironmentOwnedFields(candidate, desired)`；通过条件读取后修改 `candidate` | `/root/Lite2API/internal/config/config.go:902`、`/root/Lite2API/internal/config/config.go:958`、`/root/Lite2API/internal/config/config.go:959`、`/root/Lite2API/internal/config/config.go:962`、`/root/Lite2API/internal/config/config.go:968` |
| I-S20 | `headers_env` 指定的环境项 | 配置校验 | 环境变量读取 | 每次配置校验，每个 Header 环境项一次 | Header 值长度 | `Config.Validate()`；通过 `os.Getenv(envName)` 直接接收 | `/root/Lite2API/internal/config/config.go:436`、`/root/Lite2API/internal/config/config.go:579` |
| I-S21 | Linux 内核 CSPRNG | 模型请求或提示测试 | `crypto/rand.Read` | 每次上游请求一次 | 12 字节 request ID 随机量 | `requestID()`；通过局部数组 `data` 接收 | `/root/Lite2API/internal/gateway/gateway.go:515`、`/root/Lite2API/internal/gateway/gateway.go:1403`、`/root/Lite2API/internal/gateway/gateway.go:1404`、`/root/Lite2API/internal/gateway/gateway.go:1405`；`/root/Lite2API/internal/gateway/prompttest.go:145` |
| I-S22 | Linux 内核 CSPRNG | 客户端 Key 创建 | `crypto/rand.Read` | 每次 Key 创建两次 | 8 字节 ID、32 字节 secret | `ClientKeyStore.Create(input)`；通过 `idBytes`、`secretBytes` 接收 | `/root/Lite2API/internal/gateway/clientkeys.go:273`、`/root/Lite2API/internal/gateway/clientkeys.go:282`、`/root/Lite2API/internal/gateway/clientkeys.go:283`、`/root/Lite2API/internal/gateway/clientkeys.go:286` |
| I-S23 | Linux 内核 CSPRNG | 管理会话签发 | `crypto/rand.Read` | 每次会话签发两次 | 32 字节 session token、24 字节 CSRF token | `randomURLToken(bytes int)`；通过局部 `value` 接收 | `/root/Lite2API/internal/gateway/adminauth.go:169`、`/root/Lite2API/internal/gateway/adminauth.go:174`、`/root/Lite2API/internal/gateway/adminauth.go:274`、`/root/Lite2API/internal/gateway/adminauth.go:276` |

## 2. 真实函数外部调用出口

### 2.1 HTTP 返回：公共状态层（6 条）

接收者均为发起本次连接的单个 HTTP peer；生产公网场景是 Nginx worker。频率均为每次请求一次。

| ID | 产生函数 / 输出方式 | 数据本身 | 单数接收者 | 失败分支去向 | 证据 |
|---|---|---|---|---|---|
| O-P01 | `serveHealth`；直接 HTTP JSON | 存活、路由健康、账号数、模型数 | 当前 HTTP peer | 同一 peer 收到 503 JSON；写失败无备用去向 | `/root/Lite2API/internal/gateway/server.go:174`、`/root/Lite2API/internal/gateway/server.go:178`、`/root/Lite2API/internal/gateway/server.go:182` |
| O-P02 | `serveHealth`；直接 HTTP JSON | 与 `/health` 相同的详细健康快照 | 当前 HTTP peer | 同 O-P01 | `/root/Lite2API/internal/gateway/server.go:174`、`/root/Lite2API/internal/gateway/server.go:182` |
| O-P03 | `serveLiveness`；直接 HTTP JSON | `status=ok`、服务名 | 当前 HTTP peer | 无业务失败分支；写失败无备用去向 | `/root/Lite2API/internal/gateway/server.go:86`、`/root/Lite2API/internal/gateway/server.go:87` |
| O-P04 | `serveReadiness`；直接 HTTP JSON | 路由结构可用性、观测新鲜度、失败路由 | 当前 HTTP peer | 同一 peer 收到 503 JSON；写失败无备用去向 | `/root/Lite2API/internal/gateway/server.go:90`、`/root/Lite2API/internal/gateway/server.go:113`、`/root/Lite2API/internal/gateway/server.go:121`、`/root/Lite2API/internal/gateway/server.go:161` |
| O-P05 | `serveAdminPage`；直接 HTTP HTML，可 gzip | 自包含管理应用 | 当前 HTTP peer | 同一 peer 收到 404 或 405；写失败无备用去向 | `/root/Lite2API/internal/gateway/server.go:192`、`/root/Lite2API/internal/gateway/server.go:194`、`/root/Lite2API/internal/gateway/server.go:198`、`/root/Lite2API/internal/gateway/server.go:205`、`/root/Lite2API/internal/gateway/server.go:207`、`/root/Lite2API/internal/gateway/server.go:210` |
| O-P06 | `Run` 根路径闭包；直接 HTTP 307 | `Location: /admin` | 当前 HTTP peer | 同一 peer 收到 404；写失败无备用去向 | `/root/Lite2API/internal/gateway/server.go:36`、`/root/Lite2API/internal/gateway/server.go:38`、`/root/Lite2API/internal/gateway/server.go:42`、`/root/Lite2API/internal/gateway/server.go:46` |

### 2.2 HTTP 返回：模型协议层（8 条）

| ID | 产生函数 / 输出方式 | 数据本身 | 单数接收者 | 失败分支去向 | 证据 |
|---|---|---|---|---|---|
| O-G01 | `serveModels`；直接 HTTP JSON | Key 过滤后的模型目录 | 当前 HTTP peer | `ServeGateway` 向同一 peer 返回 401/405 | `/root/Lite2API/internal/gateway/gateway.go:364`、`/root/Lite2API/internal/gateway/gateway.go:380`、`/root/Lite2API/internal/gateway/gateway.go:383`、`/root/Lite2API/internal/gateway/gateway.go:1115`、`/root/Lite2API/internal/gateway/gateway.go:1124` |
| O-G02 | `ServeGateway`；转发上游 HTTP | Chat 响应或流 | 当前 HTTP peer | 同一 peer 收到 OpenAI 风格 4xx/5xx；流已开始后中断连接 | `/root/Lite2API/internal/gateway/gateway.go:358`、`/root/Lite2API/internal/gateway/gateway.go:694`、`/root/Lite2API/internal/gateway/gateway.go:743`、`/root/Lite2API/internal/gateway/gateway.go:767` |
| O-G03 | `ServeGateway`；转发上游 HTTP | Responses 响应或 SSE 流 | 当前 HTTP peer | 同 O-G02 | `/root/Lite2API/internal/gateway/gateway.go:358`、`/root/Lite2API/internal/gateway/gateway.go:701`、`/root/Lite2API/internal/gateway/gateway.go:714`、`/root/Lite2API/internal/gateway/gateway.go:769` |
| O-G04 | `ServeGateway`；转发上游 HTTP | Anthropic Messages 响应或流 | 当前 HTTP peer | 同一 peer 收到 Anthropic 风格错误；流已开始后中断连接 | `/root/Lite2API/internal/gateway/gateway.go:360`、`/root/Lite2API/internal/gateway/gateway.go:362`、`/root/Lite2API/internal/gateway/gateway.go:769`、`/root/Lite2API/internal/gateway/gateway.go:1797` |
| O-G05 | `ServeGateway`；转发上游 HTTP | Embedding 向量响应 | 当前 HTTP peer | 同一 peer 收到 4xx/5xx；不确定提交返回 502 | `/root/Lite2API/internal/gateway/gateway.go:701`、`/root/Lite2API/internal/gateway/gateway.go:709`、`/root/Lite2API/internal/gateway/gateway.go:714`、`/root/Lite2API/internal/gateway/gateway.go:769` |
| O-G06 | `ServeGateway`；转发上游 HTTP | 图像生成响应 | 当前 HTTP peer | 同一 peer 收到 4xx/5xx；不确定提交返回 502 | `/root/Lite2API/internal/gateway/gateway.go:701`、`/root/Lite2API/internal/gateway/gateway.go:714`、`/root/Lite2API/internal/gateway/gateway.go:750`、`/root/Lite2API/internal/gateway/gateway.go:769` |
| O-G07 | `ServeGateway`；转发上游 HTTP | 重排结果 | 当前 HTTP peer | 同一 peer 收到 4xx/5xx；不确定提交返回 502 | `/root/Lite2API/internal/gateway/gateway.go:701`、`/root/Lite2API/internal/gateway/gateway.go:714`、`/root/Lite2API/internal/gateway/gateway.go:769` |
| O-G08 | `ServeGateway`；直接协议错误 JSON | 405 方法拒绝或 404 路径拒绝 | 当前 HTTP peer | 无后续分支；响应写失败无备用去向 | `/root/Lite2API/internal/gateway/gateway.go:380`、`/root/Lite2API/internal/gateway/gateway.go:383`、`/root/Lite2API/internal/gateway/gateway.go:390`、`/root/Lite2API/internal/gateway/gateway.go:392`、`/root/Lite2API/internal/gateway/gateway.go:395`、`/root/Lite2API/internal/gateway/gateway.go:397` |

### 2.3 HTTP 返回：管理层（29 条）

接收者均为当前单个 HTTP peer。通用失败分支先返回网络拒绝、401、403 或 404 JSON：`/root/Lite2API/internal/gateway/admin.go:21`、`/root/Lite2API/internal/gateway/admin.go:22`、`/root/Lite2API/internal/gateway/admin.go:38`、`/root/Lite2API/internal/gateway/admin.go:40`、`/root/Lite2API/internal/gateway/admin.go:43`、`/root/Lite2API/internal/gateway/admin.go:44`、`/root/Lite2API/internal/gateway/admin.go:189`、`/root/Lite2API/internal/gateway/admin.go:190`。

| ID | 产生函数 / 输出方式 | 具体输出数据 | 失败分支去向 | 证据 |
|---|---|---|---|---|
| O-A01 | `serveAdminLogin`；直接 HTTP JSON、Set-Cookie | CSRF、有效秒数、会话 Cookie | 同一 peer：401 或 429 JSON | `/root/Lite2API/internal/gateway/admin.go:250`、`/root/Lite2API/internal/gateway/admin.go:252`、`/root/Lite2API/internal/gateway/admin.go:255`、`/root/Lite2API/internal/gateway/admin.go:260`、`/root/Lite2API/internal/gateway/admin.go:261` |
| O-A02 | `ServeAdminAPI`；直接 HTTP JSON | `ok`、CSRF、会话标志 | 同一 peer：通用认证错误 | `/root/Lite2API/internal/gateway/admin.go:48`、`/root/Lite2API/internal/gateway/admin.go:49` |
| O-A03 | `ServeAdminAPI`；直接 HTTP JSON、失效 Cookie | `ok=true` | 同一 peer：通用认证或 CSRF 错误 | `/root/Lite2API/internal/gateway/admin.go:50`、`/root/Lite2API/internal/gateway/admin.go:52`、`/root/Lite2API/internal/gateway/admin.go:53` |
| O-A04 | `ServeAdminAPI`；直接 HTTP JSON | 统计、操作健康、日志状态、账号快照、模型、脱敏配置 | 同一 peer：通用认证错误 | `/root/Lite2API/internal/gateway/admin.go:54`、`/root/Lite2API/internal/gateway/admin.go:56` |
| O-A05 | `ServeAdminAPI`；直接 HTTP JSON | 时间范围趋势点 | 同一 peer：400 或通用认证错误 | `/root/Lite2API/internal/gateway/admin.go:57`、`/root/Lite2API/internal/gateway/admin.go:60`、`/root/Lite2API/internal/gateway/admin.go:63` |
| O-A06 | `ServeAdminAPI`；直接 HTTP JSON | 适配器目录、运行态探针结果 | 同一 peer：通用认证错误 | `/root/Lite2API/internal/gateway/admin.go:64`、`/root/Lite2API/internal/gateway/admin.go:65` |
| O-A07 | `servePromptTest`；直接 HTTP JSON | 账号、上游模型、request ID、耗时、上游 JSON | 同一 peer：400/404/409/429/502 或上游状态 | `/root/Lite2API/internal/gateway/prompttest.go:43`、`/root/Lite2API/internal/gateway/prompttest.go:81`、`/root/Lite2API/internal/gateway/prompttest.go:98`、`/root/Lite2API/internal/gateway/prompttest.go:147`、`/root/Lite2API/internal/gateway/prompttest.go:163`、`/root/Lite2API/internal/gateway/prompttest.go:179` |
| O-A08 | `ServeAdminAPI`；直接 HTTP JSON | 托管 Key 列表，不含明文 secret | 同一 peer：通用认证错误 | `/root/Lite2API/internal/gateway/admin.go:68`、`/root/Lite2API/internal/gateway/admin.go:69` |
| O-A09 | `ServeAdminAPI`；直接 HTTP JSON | 新 Key 元数据、仅一次明文 secret | 同一 peer：400 JSON；落盘失败同一 peer 收到错误 | `/root/Lite2API/internal/gateway/admin.go:70`、`/root/Lite2API/internal/gateway/admin.go:75`、`/root/Lite2API/internal/gateway/admin.go:77`、`/root/Lite2API/internal/gateway/admin.go:80` |
| O-A10 | `ServeAdminAPI`；直接 HTTP JSON | 更新后 Key 元数据 | 同一 peer：400 JSON；落盘失败同一 peer 收到错误 | `/root/Lite2API/internal/gateway/admin.go:81`、`/root/Lite2API/internal/gateway/admin.go:87`、`/root/Lite2API/internal/gateway/admin.go:89`、`/root/Lite2API/internal/gateway/admin.go:92` |
| O-A11 | `ServeAdminAPI`；直接 HTTP JSON | `ok=true` | 同一 peer：400/404 JSON；落盘失败同一 peer 收到错误 | `/root/Lite2API/internal/gateway/admin.go:93`、`/root/Lite2API/internal/gateway/admin.go:96`、`/root/Lite2API/internal/gateway/admin.go:99`、`/root/Lite2API/internal/gateway/admin.go:103` |
| O-A12 | `ServeAdminAPI`；直接 HTTP JSON | `ok=true` | 同一 peer：400 配置错误 | `/root/Lite2API/internal/gateway/admin.go:104`、`/root/Lite2API/internal/gateway/admin.go:106`、`/root/Lite2API/internal/gateway/admin.go:109` |
| O-A13 | `serveOAuthStart`；直接 HTTP JSON | 状态、授权 URL、state、provider、回调要求 | 同一 peer：400 或 502 JSON | `/root/Lite2API/internal/gateway/oauthadapter.go:169`、`/root/Lite2API/internal/gateway/oauthadapter.go:177`、`/root/Lite2API/internal/gateway/oauthadapter.go:185`、`/root/Lite2API/internal/gateway/oauthadapter.go:190`、`/root/Lite2API/internal/gateway/oauthadapter.go:193` |
| O-A14 | `serveOAuthCallback`；直接 HTTP JSON | `ok`、适配器状态 | 同一 peer：400 或适配器映射错误 | `/root/Lite2API/internal/gateway/oauthadapter.go:199`、`/root/Lite2API/internal/gateway/oauthadapter.go:207`、`/root/Lite2API/internal/gateway/oauthadapter.go:218`、`/root/Lite2API/internal/gateway/oauthadapter.go:222` |
| O-A15 | `serveOAuthStatus`；直接 HTTP JSON | 状态、错误、pool 状态、凭据数 | 同一 peer：400 或适配器映射错误；pool 失败作为 warning | `/root/Lite2API/internal/gateway/oauthadapter.go:225`、`/root/Lite2API/internal/gateway/oauthadapter.go:233`、`/root/Lite2API/internal/gateway/oauthadapter.go:237`、`/root/Lite2API/internal/gateway/oauthadapter.go:245`、`/root/Lite2API/internal/gateway/oauthadapter.go:248`、`/root/Lite2API/internal/gateway/oauthadapter.go:261` |
| O-A16 | `serveOAuthAccounts`；直接 HTTP JSON | 脱敏 OAuth 凭据、额度、健康摘要 | 同一 peer：适配器映射错误 | `/root/Lite2API/internal/gateway/oauthadapter.go:264`、`/root/Lite2API/internal/gateway/oauthadapter.go:265`、`/root/Lite2API/internal/gateway/oauthadapter.go:270` |
| O-A17 | `serveOAuthAccountRefresh`；直接 HTTP JSON | 刷新数、跳过数、文件结果、失败项 | 同一 peer：适配器映射错误 | `/root/Lite2API/internal/gateway/oauthadapter.go:364`、`/root/Lite2API/internal/gateway/oauthadapter.go:383`、`/root/Lite2API/internal/gateway/oauthadapter.go:387` |
| O-A18 | `serveOAuthAccountPriority`；直接 HTTP JSON | `ok`、状态、id、priority | 同一 peer：400 或适配器映射错误 | `/root/Lite2API/internal/gateway/oauthadapter.go:273`、`/root/Lite2API/internal/gateway/oauthadapter.go:280`、`/root/Lite2API/internal/gateway/oauthadapter.go:295`、`/root/Lite2API/internal/gateway/oauthadapter.go:299` |
| O-A19 | `serveOAuthAccountDelete`；直接 HTTP JSON | `ok=true` | 同一 peer：400 或适配器映射错误 | `/root/Lite2API/internal/gateway/oauthadapter.go:397`、`/root/Lite2API/internal/gateway/oauthadapter.go:404`、`/root/Lite2API/internal/gateway/oauthadapter.go:409`、`/root/Lite2API/internal/gateway/oauthadapter.go:413` |
| O-A20 | `serveOAuthAccountStatus`；直接 HTTP JSON | `ok`、disabled | 同一 peer：400 或适配器映射错误 | `/root/Lite2API/internal/gateway/oauthadapter.go:416`、`/root/Lite2API/internal/gateway/oauthadapter.go:423`、`/root/Lite2API/internal/gateway/oauthadapter.go:436`、`/root/Lite2API/internal/gateway/oauthadapter.go:440` |
| O-A21 | `serveOAuthRouting`；直接 HTTP JSON | 策略、优先级方向、自动故障切换标志 | 同一 peer：502 或适配器映射错误 | `/root/Lite2API/internal/gateway/oauthadapter.go:304`、`/root/Lite2API/internal/gateway/oauthadapter.go:308`、`/root/Lite2API/internal/gateway/oauthadapter.go:314`、`/root/Lite2API/internal/gateway/oauthadapter.go:317`、`/root/Lite2API/internal/gateway/oauthadapter.go:344` |
| O-A22 | `serveOAuthRoutingUpdate`；直接 HTTP JSON | 更新后的策略摘要 | 同一 peer：400 或适配器映射错误 | `/root/Lite2API/internal/gateway/oauthadapter.go:320`、`/root/Lite2API/internal/gateway/oauthadapter.go:326`、`/root/Lite2API/internal/gateway/oauthadapter.go:337`、`/root/Lite2API/internal/gateway/oauthadapter.go:341` |
| O-A23 | `serveAccountTest`；直接 HTTP JSON | 可用性、状态、耗时、端点、模型数 | 同一 peer：400 或 502 JSON | `/root/Lite2API/internal/gateway/accounttest.go:38`、`/root/Lite2API/internal/gateway/accounttest.go:67`、`/root/Lite2API/internal/gateway/accounttest.go:77`、`/root/Lite2API/internal/gateway/accounttest.go:79`、`/root/Lite2API/internal/gateway/accounttest.go:82` |
| O-A24 | `ServeAdminAPI`；直接 HTTP JSON | 可恢复账号导出数据，可选代理定义 | 同一 peer：400 JSON | `/root/Lite2API/internal/gateway/admin.go:132`、`/root/Lite2API/internal/gateway/admin.go:137`、`/root/Lite2API/internal/gateway/admin.go:139`、`/root/Lite2API/internal/gateway/admin.go:142` |
| O-A25 | `ServeAdminAPI`；直接 HTTP JSON | 导入创建、更新、跳过、失败明细 | 同一 peer：400/409 JSON；部分 OAuth 失败留在结果 errors | `/root/Lite2API/internal/gateway/admin.go:143`、`/root/Lite2API/internal/gateway/admin.go:148`、`/root/Lite2API/internal/gateway/admin.go:150`、`/root/Lite2API/internal/gateway/admin.go:154`、`/root/Lite2API/internal/gateway/admin.go:157` |
| O-A26 | `ServeAdminAPI`；直接 HTTP JSON | `ok=true` | 同一 peer：400/409 JSON | `/root/Lite2API/internal/gateway/admin.go:158`、`/root/Lite2API/internal/gateway/admin.go:163`、`/root/Lite2API/internal/gateway/admin.go:164`、`/root/Lite2API/internal/gateway/admin.go:167` |
| O-A27 | `ServeAdminAPI`；直接 HTTP JSON | `ok=true` | 同一 peer：400/409 JSON | `/root/Lite2API/internal/gateway/admin.go:168`、`/root/Lite2API/internal/gateway/admin.go:174`、`/root/Lite2API/internal/gateway/admin.go:175`、`/root/Lite2API/internal/gateway/admin.go:178` |
| O-A28 | `ServeAdminAPI`；直接 HTTP JSON | `ok=true` | 同一 peer：400/409 JSON | `/root/Lite2API/internal/gateway/admin.go:179`、`/root/Lite2API/internal/gateway/admin.go:184`、`/root/Lite2API/internal/gateway/admin.go:185`、`/root/Lite2API/internal/gateway/admin.go:188` |
| O-A29 | `ServeAdminAPI`；直接 HTTP 404 JSON | `admin endpoint not found` | 无后续分支；响应写失败无备用去向 | `/root/Lite2API/internal/gateway/admin.go:189`、`/root/Lite2API/internal/gateway/admin.go:190` |

### 2.4 外部服务调用层（10 条）

| ID | 传递者 / 输出方式 | 数据本身 | 单数接收者 | 失败分支去向 | 证据 |
|---|---|---|---|---|---|
| O-U01 | `doUpstream`；直接 HTTP 请求 | 改写后的模型请求、受控 Header、账号认证、request ID | 当前 CLIProxyAPI `127.0.0.1:45682` | 返回 `ServeGateway` 或 `servePromptTest`；函数返回 `upstreamRequestError` | `/root/Lite2API/internal/gateway/gateway.go:986`、`/root/Lite2API/internal/gateway/gateway.go:1017`、`/root/Lite2API/internal/gateway/gateway.go:1028`、`/root/Lite2API/internal/gateway/gateway.go:1031`、`/root/Lite2API/internal/gateway/gateway.go:1048`、`/root/Lite2API/internal/gateway/gateway.go:1056` |
| O-U02 | `fetchDiscoveredCatalog`；直接 HTTP GET | `/v1/models` 请求，可带 `client_version=lite2api` | 当前 CLIProxyAPI | 返回 `syncDiscoveredCapabilities`；错误记失败并继续其他账号 | `/root/Lite2API/internal/gateway/capabilitysync.go:314`、`/root/Lite2API/internal/gateway/capabilitysync.go:320`、`/root/Lite2API/internal/gateway/capabilitysync.go:324`、`/root/Lite2API/internal/gateway/capabilitysync.go:330`、`/root/Lite2API/internal/gateway/capabilitysync.go:349`；`/root/Lite2API/internal/gateway/capabilitysync.go:72`、`/root/Lite2API/internal/gateway/capabilitysync.go:74` |
| O-U03 | `probeAccountModels`；直接 HTTP GET | 管理员提交账号的 `/models` 探针、认证 Header | 被测试账号 URL 对应的单个服务 | 返回 `serveAccountTest`；错误由同一管理 HTTP peer 收到 502 | `/root/Lite2API/internal/gateway/accounttest.go:95`、`/root/Lite2API/internal/gateway/accounttest.go:100`、`/root/Lite2API/internal/gateway/accounttest.go:114`、`/root/Lite2API/internal/gateway/accounttest.go:125`、`/root/Lite2API/internal/gateway/accounttest.go:145`、`/root/Lite2API/internal/gateway/accounttest.go:151` |
| O-U04 | `adapterProbeCache.probeNow`；直接 HTTP GET | 根就绪探针 | AtomCode2API `127.0.0.1:45678` | 返回 `AdapterCatalog` 的 stopped/unavailable 状态 | `/root/Lite2API/internal/gateway/adapters.go:55`、`/root/Lite2API/internal/gateway/adapters.go:265`、`/root/Lite2API/internal/gateway/adapters.go:272`、`/root/Lite2API/internal/gateway/adapters.go:277`、`/root/Lite2API/internal/gateway/adapters.go:279` |
| O-U05 | `adapterProbeCache.probeNow`；直接 HTTP GET | 根就绪探针 | Grok2API `127.0.0.1:45680` | 返回 `AdapterCatalog` 的 stopped/unavailable 状态 | `/root/Lite2API/internal/gateway/adapters.go:56`、`/root/Lite2API/internal/gateway/adapters.go:265`、`/root/Lite2API/internal/gateway/adapters.go:272`、`/root/Lite2API/internal/gateway/adapters.go:277`、`/root/Lite2API/internal/gateway/adapters.go:279` |
| O-U06 | `adapterProbeCache.probeNow`；直接 HTTP GET | 根就绪探针 | Gemini-Web2API `127.0.0.1:45681` | 返回 `AdapterCatalog` 的 stopped/unavailable 状态 | `/root/Lite2API/internal/gateway/adapters.go:57`、`/root/Lite2API/internal/gateway/adapters.go:265`、`/root/Lite2API/internal/gateway/adapters.go:272`、`/root/Lite2API/internal/gateway/adapters.go:277`、`/root/Lite2API/internal/gateway/adapters.go:279` |
| O-U07 | `adapterProbeCache.probeNow`；直接 HTTP GET | 根就绪探针 | CLIProxyAPI `127.0.0.1:45682` | 返回 `AdapterCatalog` 的 stopped/unavailable 状态 | `/root/Lite2API/internal/gateway/adapters.go:58`、`/root/Lite2API/internal/gateway/adapters.go:265`、`/root/Lite2API/internal/gateway/adapters.go:272`、`/root/Lite2API/internal/gateway/adapters.go:277`、`/root/Lite2API/internal/gateway/adapters.go:279` |
| O-U08 | `adapterProbeCache.adapterModels`；直接 HTTP GET | `/models` 鉴权探针 | CLIProxyAPI `127.0.0.1:45682` | 返回 `probeNow`；错误转为 `ready=false`，不另行输出 | `/root/Lite2API/internal/gateway/adapters.go:58`、`/root/Lite2API/internal/gateway/adapters.go:307`、`/root/Lite2API/internal/gateway/adapters.go:315`、`/root/Lite2API/internal/gateway/adapters.go:320`、`/root/Lite2API/internal/gateway/adapters.go:321` |
| O-U09 | `callOAuthAdapter`；直接 HTTP 请求 | OAuth 会话、回调、状态、auth-file、刷新、优先级、路由策略管理数据 | 当前 CLIProxyAPI 管理 API | 返回对应 OAuth 管理处理函数；错误映射到同一管理 HTTP peer | `/root/Lite2API/internal/gateway/oauthadapter.go:591`、`/root/Lite2API/internal/gateway/oauthadapter.go:601`、`/root/Lite2API/internal/gateway/oauthadapter.go:611`、`/root/Lite2API/internal/gateway/oauthadapter.go:620`、`/root/Lite2API/internal/gateway/oauthadapter.go:622`、`/root/Lite2API/internal/gateway/oauthadapter.go:637` |
| O-U10 | `discoverOAuthModels`；直接 HTTP GET | 带 `CLIPROXYAPI_KEY` 的 `/v1/models` 请求 | 当前 CLIProxyAPI | 返回 `ensureOAuthPoolAccount`；错误作为 OAuth status warning 或 import error | `/root/Lite2API/internal/gateway/oauthadapter.go:713`、`/root/Lite2API/internal/gateway/oauthadapter.go:715`、`/root/Lite2API/internal/gateway/oauthadapter.go:749`、`/root/Lite2API/internal/gateway/oauthadapter.go:755`、`/root/Lite2API/internal/gateway/oauthadapter.go:760`、`/root/Lite2API/internal/gateway/oauthadapter.go:761` |

### 2.5 文件、日志、进程终态层（12 条）

| ID | 传递者 / 输出方式 | 数据本身 | 单数接收者 | 失败分支去向 | 证据 |
|---|---|---|---|---|---|
| O-S01 | `config.Store.saveLocked`；临时文件写入、fsync、rename、目录 fsync | 期望配置 JSON，环境覆盖字段不落盘 | `/etc/lite2api/config.json` | 返回管理调用方或发现协程；运行态提交不发生 | `/root/Lite2API/internal/config/config.go:906`、`/root/Lite2API/internal/config/config.go:914`、`/root/Lite2API/internal/config/config.go:923`、`/root/Lite2API/internal/config/config.go:933`、`/root/Lite2API/internal/config/config.go:937`、`/root/Lite2API/internal/config/config.go:944`、`/root/Lite2API/internal/config/config.go:951`；`/root/Lite2API/internal/gateway/gateway.go:208` |
| O-S02 | `ClientKeyStore.persist`；临时文件写入、fsync、rename | 仅 Key 摘要、元数据、策略 | `/etc/lite2api/client_keys.json` | 返回对应管理 HTTP peer；内存快照不切换 | `/root/Lite2API/internal/gateway/clientkeys.go:499`、`/root/Lite2API/internal/gateway/clientkeys.go:505`、`/root/Lite2API/internal/gateway/clientkeys.go:514`、`/root/Lite2API/internal/gateway/clientkeys.go:524`、`/root/Lite2API/internal/gateway/clientkeys.go:528`、`/root/Lite2API/internal/gateway/clientkeys.go:535` |
| O-S03 | `requestLogWriter.write`；NDJSON append，达到上限时 rotate | 脱敏请求摘要；不含正文或密钥 | `/etc/lite2api/request.log` | `requestLogWriter.run` 写 JSON 错误日志；队列满时计 dropped | `/root/Lite2API/internal/gateway/requestlog.go:131`、`/root/Lite2API/internal/gateway/requestlog.go:144`、`/root/Lite2API/internal/gateway/requestlog.go:145`、`/root/Lite2API/internal/gateway/requestlog.go:201`、`/root/Lite2API/internal/gateway/requestlog.go:202`、`/root/Lite2API/internal/gateway/requestlog.go:221`、`/root/Lite2API/internal/gateway/requestlog.go:234`、`/root/Lite2API/internal/gateway/requestlog.go:269` |
| O-S04 | `main`；stdout JSON 日志 | 启动失败、服务停止错误 | [推断] systemd journal；依据 stdout handler和 systemd 服务启动 | slog handler 错误无业务备用去向 | `/root/Lite2API/cmd/lite2api/main.go:34`、`/root/Lite2API/cmd/lite2api/main.go:37`、`/root/Lite2API/cmd/lite2api/main.go:42`；`/etc/systemd/system/lite2api.service:13` |
| O-S05 | `Gateway.Run`；stdout JSON 日志 | 监听地址、重载成功、重载失败 | [推断] systemd journal；依据 stdout handler和 systemd 服务启动 | slog handler 错误无业务备用去向 | `/root/Lite2API/internal/gateway/server.go:18`、`/root/Lite2API/internal/gateway/server.go:52`、`/root/Lite2API/internal/gateway/server.go:75`、`/root/Lite2API/internal/gateway/server.go:77`；`/root/Lite2API/cmd/lite2api/main.go:34` |
| O-S06 | `Gateway.New`；stdout JSON 日志 | 请求证据恢复失败 | [推断] systemd journal；依据 stdout handler和 systemd 服务启动 | slog handler 错误无业务备用去向 | `/root/Lite2API/internal/gateway/gateway.go:75`、`/root/Lite2API/internal/gateway/gateway.go:101`、`/root/Lite2API/internal/gateway/gateway.go:102`；`/root/Lite2API/cmd/lite2api/main.go:34` |
| O-S07 | `recoverer`；stdout JSON 日志 | panic 值、stack | [推断] systemd journal；依据 stdout handler和 systemd 服务启动 | 同一 HTTP peer 另收 500；日志自身无备用去向 | `/root/Lite2API/internal/gateway/server.go:241`、`/root/Lite2API/internal/gateway/server.go:244`、`/root/Lite2API/internal/gateway/server.go:245`、`/root/Lite2API/internal/gateway/server.go:247`；`/root/Lite2API/cmd/lite2api/main.go:34` |
| O-S08 | `requestLogWriter.run`；stdout JSON 日志 | 请求摘要写盘错误 | [推断] systemd journal；依据 stdout handler和 systemd 服务启动 | 日志自身无备用去向 | `/root/Lite2API/internal/gateway/requestlog.go:131`、`/root/Lite2API/internal/gateway/requestlog.go:144`、`/root/Lite2API/internal/gateway/requestlog.go:145`；`/root/Lite2API/cmd/lite2api/main.go:34` |
| O-S09 | `runCapabilityDiscovery`；stdout JSON 日志 | 一轮能力发现失败摘要 | [推断] systemd journal；依据 stdout handler和 systemd 服务启动 | 日志自身无备用去向 | `/root/Lite2API/internal/gateway/capabilitysync.go:36`、`/root/Lite2API/internal/gateway/capabilitysync.go:44`、`/root/Lite2API/internal/gateway/capabilitysync.go:45`；`/root/Lite2API/cmd/lite2api/main.go:34` |
| O-S10 | `syncDiscoveredCapabilities`；stdout JSON 日志 | 单账号跳过、同步账号数 | [推断] systemd journal；依据 stdout handler和 systemd 服务启动 | 日志自身无备用去向 | `/root/Lite2API/internal/gateway/capabilitysync.go:52`、`/root/Lite2API/internal/gateway/capabilitysync.go:72`、`/root/Lite2API/internal/gateway/capabilitysync.go:74`、`/root/Lite2API/internal/gateway/capabilitysync.go:131`、`/root/Lite2API/internal/gateway/capabilitysync.go:132`；`/root/Lite2API/cmd/lite2api/main.go:34` |
| O-S11 | `Gateway.LogState`；stdout JSON 日志 | 已加载账号数、模型数 | [推断] systemd journal；依据 stdout handler和 systemd 服务启动 | 日志自身无备用去向 | `/root/Lite2API/internal/gateway/gateway.go:1832`、`/root/Lite2API/internal/gateway/gateway.go:1834`；`/root/Lite2API/cmd/lite2api/main.go:34`、`/root/Lite2API/cmd/lite2api/main.go:40` |
| O-S12 | `main`；返回或 `os.Exit(1)` | 正常终止或失败退出码 | `systemd` 服务管理器 | 失败触发 3 秒后重启 | `/root/Lite2API/cmd/lite2api/main.go:16`、`/root/Lite2API/cmd/lite2api/main.go:29`、`/root/Lite2API/cmd/lite2api/main.go:38`、`/root/Lite2API/cmd/lite2api/main.go:41`、`/root/Lite2API/cmd/lite2api/main.go:43`；`/etc/systemd/system/lite2api.service:14`、`/etc/systemd/system/lite2api.service:15` |

### 2.6 CLI 诊断层（1 条）

| ID | 传递者 / 输出方式 | 数据本身 | 单数接收者 | 失败分支去向 | 证据 |
|---|---|---|---|---|---|
| O-C01 | `main`；stdout 或 stderr、进程返回 | 版本字符串；或配置账号数、路由数 | 启动诊断命令的 shell 进程 | 配置无效时同一 shell 收到 stderr 和退出码 1 | `/root/Lite2API/cmd/lite2api/main.go:21`、`/root/Lite2API/cmd/lite2api/main.go:22`、`/root/Lite2API/cmd/lite2api/main.go:25`、`/root/Lite2API/cmd/lite2api/main.go:28`、`/root/Lite2API/cmd/lite2api/main.go:29`、`/root/Lite2API/cmd/lite2api/main.go:31`、`/root/Lite2API/cmd/lite2api/main.go:32` |

## 3. 内部核心模块（7 个）

| ID | 模块 | 解决的问题 | 如何处理外部数据 | 入口数据 | 职责阶段 | 证据 |
|---|---|---|---|---|---|---|
| M01 | 生命周期装配 | 从配置、环境、持久化快照构造一致运行态，响应重载和停机 | `New` 加载配置、日志、Key；`Run` 监听 HTTP 和信号；`commitStateLocked` 原子切换状态 | I-S01～I-S07、I-S10～I-S12、I-S14、I-C01 | 执行保障脚手架 | `/root/Lite2API/internal/gateway/gateway.go:75`、`/root/Lite2API/internal/gateway/gateway.go:83`、`/root/Lite2API/internal/gateway/gateway.go:91`、`/root/Lite2API/internal/gateway/gateway.go:115`、`/root/Lite2API/internal/gateway/gateway.go:147`、`/root/Lite2API/internal/gateway/gateway.go:227`；`/root/Lite2API/internal/gateway/server.go:18`、`/root/Lite2API/internal/gateway/server.go:48`、`/root/Lite2API/internal/gateway/server.go:61` |
| M02 | 边界鉴权 | 隔离模型 Key、管理 Token、CIDR、Cookie、CSRF | 模型面鉴别 Key、RPM、并发、模型权限；管理面检查网络、会话、CSRF | I-S04、I-S05、I-S10、I-S23、I-P01～I-P06、I-G01～I-G08、I-A01～I-A29 | 决策输入装配 | `/root/Lite2API/internal/gateway/gateway.go:364`、`/root/Lite2API/internal/gateway/gateway.go:369`、`/root/Lite2API/internal/gateway/gateway.go:373`、`/root/Lite2API/internal/gateway/gateway.go:485`；`/root/Lite2API/internal/gateway/admin.go:21`、`/root/Lite2API/internal/gateway/admin.go:38`、`/root/Lite2API/internal/gateway/admin.go:43` |
| M03 | 请求编排 | 把不同模型协议请求安全转换为一次或有限次上游尝试 | 限制请求体，解析 model/session，应用执行档位，改写上游模型，处理重试安全性，流式回传 | I-S21、I-G02～I-G07、I-R01 | 决策核心 | `/root/Lite2API/internal/gateway/gateway.go:400`、`/root/Lite2API/internal/gateway/gateway.go:429`、`/root/Lite2API/internal/gateway/gateway.go:471`、`/root/Lite2API/internal/gateway/gateway.go:489`、`/root/Lite2API/internal/gateway/gateway.go:510`、`/root/Lite2API/internal/gateway/gateway.go:585`、`/root/Lite2API/internal/gateway/gateway.go:625`、`/root/Lite2API/internal/gateway/gateway.go:649`、`/root/Lite2API/internal/gateway/gateway.go:701`、`/root/Lite2API/internal/gateway/gateway.go:769` |
| M04 | 路由调度 | 在能力、健康、容量、策略约束内选出单个目标 | `Scheduler.Select` 过滤 operation/model，等待容量，返回 Selection；结果更新 breaker | I-G02～I-G07、I-R01、I-R04 | 决策核心 | `/root/Lite2API/internal/gateway/runtime.go:573`、`/root/Lite2API/internal/gateway/runtime.go:637`、`/root/Lite2API/internal/gateway/runtime.go:678`、`/root/Lite2API/internal/gateway/runtime.go:802`；`/root/Lite2API/internal/gateway/gateway.go:585`、`/root/Lite2API/internal/gateway/gateway.go:648`、`/root/Lite2API/internal/gateway/gateway.go:660` |
| M05 | 管理控制面 | 把管理命令变成可验证、可持久化、原子生效的状态变化 | 分派 29 个端点；变更前重读磁盘并检测冲突；构建新状态，先落盘后切换 | I-S13、I-S19、I-S20、I-S22、I-A01～I-A29 | 生效写世界 | `/root/Lite2API/internal/gateway/admin.go:19`、`/root/Lite2API/internal/gateway/admin.go:47`、`/root/Lite2API/internal/gateway/admin.go:368`、`/root/Lite2API/internal/gateway/admin.go:371`、`/root/Lite2API/internal/gateway/admin.go:376`、`/root/Lite2API/internal/gateway/admin.go:387`、`/root/Lite2API/internal/gateway/admin.go:391` |
| M06 | 适配器集成 | 隔离 OAuth 管理、连接测试、提示测试、能力发现、适配器就绪探针 | 仅访问明确 URL；限制超时、响应大小、并行探针；对返回目录做过滤和能力合并 | I-S08、I-S09、I-S16～I-S18、I-A06、I-A07、I-A13～I-A23、I-A25、I-R02～I-R08 | 执行保障脚手架 | `/root/Lite2API/internal/gateway/adapters.go:141`、`/root/Lite2API/internal/gateway/adapters.go:142`、`/root/Lite2API/internal/gateway/adapters.go:161`；`/root/Lite2API/internal/gateway/oauthadapter.go:591`、`/root/Lite2API/internal/gateway/oauthadapter.go:664`；`/root/Lite2API/internal/gateway/accounttest.go:95`；`/root/Lite2API/internal/gateway/prompttest.go:36`；`/root/Lite2API/internal/gateway/capabilitysync.go:36` |
| M07 | 可观测持久化 | 用有界、脱敏证据支撑管理状态、趋势、健康判断和重启恢复 | 记录内存统计、每路由最近结果，异步写 NDJSON，启动恢复；输出健康和管理快照 | I-S11、I-S14、I-S15、I-P01～I-P04、I-A04～I-A05、I-R01 | 证据只读验证 | `/root/Lite2API/internal/gateway/gateway.go:541`、`/root/Lite2API/internal/gateway/gateway.go:554`、`/root/Lite2API/internal/gateway/gateway.go:556`、`/root/Lite2API/internal/gateway/gateway.go:559`；`/root/Lite2API/internal/gateway/requestlog.go:19`、`/root/Lite2API/internal/gateway/requestlog.go:20`、`/root/Lite2API/internal/gateway/requestlog.go:269`、`/root/Lite2API/internal/gateway/requestlog.go:329`；`/root/Lite2API/internal/gateway/server.go:93`、`/root/Lite2API/internal/gateway/server.go:97` |

## 4. Mermaid 流程图

图按第二层子图控制规模；每个 `L-*` 子图的外部输入节点加外部输出节点均不超过 12。模块节点只定义一次。节点名没有使用“与 / 及 / 和 / 并”。边上的“HTTP·事件”表示每次 HTTP 事件；“调用返回·事件”表示每次对应函数调用。

```mermaid
flowchart LR
  subgraph CORE[内部核心]
    M01["M01 生命周期装配"]
    M02["M02 边界鉴权"]
    M03["M03 请求编排"]
    M04["M04 路由调度"]
    M05["M05 管理控制面"]
    M06["M06 适配器集成"]
    M07["M07 可观测持久化"]
  end

  subgraph LS1["L-S1 生命周期输入 12"]
    IS01["I-S01 启动参数"]
    IS02["I-S02 服务配置"]
    IS03["I-S03 运行覆盖"]
    IS04["I-S04 网关密钥"]
    IS05["I-S05 管理令牌"]
    IS06["I-S06 上游密钥"]
    IS07["I-S07 上游请求头"]
    IS08["I-S08 管理密钥"]
    IS09["I-S09 管理地址"]
    IS10["I-S10 托管密钥库"]
    IS11["I-S11 请求证据库"]
    IS12["I-S12 进程信号"]
  end
  IS01 -->|"进程启动·一次"| M01
  IS02 -->|"文件读取·事件"| M01
  IS03 -->|"环境读取·配置装配"| M01
  IS04 -->|"环境读取·配置装配"| M02
  IS05 -->|"环境读取·配置装配"| M02
  IS06 -->|"环境读取·状态装配"| M01
  IS07 -->|"环境读取·状态装配"| M01
  IS08 -->|"环境读取·管理调用"| M06
  IS09 -->|"环境读取·管理调用"| M06
  IS10 -->|"文件读取·启动一次"| M02
  IS11 -->|"文件读取·启动一次"| M07
  IS12 -->|"POSIX信号·事件"| M01
  subgraph LS3["L-S3 二级系统输入 11"]
    IS13["I-S13 期望配置"] -->|"文件读取·配置提交"| M05
    IS14["I-S14 日志元数据"] -->|"文件读取·writer打开"| M07
    IS15["I-S15 备份元数据"] -->|"文件读取·日志轮转"| M07
    IS16["I-S16 探针凭据"] -->|"环境读取·缓存未命中"| M06
    IS17["I-S17 池认证状态"] -->|"环境读取·账号池创建"| M06
    IS18["I-S18 模型认证密钥"] -->|"环境读取·模型发现"| M06
    IS19["I-S19 字段归属标记"] -->|"环境读取·配置提交"| M05
    IS20["I-S20 请求头长度"] -->|"环境读取·配置校验"| M05
    IS21["I-S21 请求标识随机量"] -->|"CSPRNG读取·上游请求"| M03
    IS22["I-S22 客户密钥随机量"] -->|"CSPRNG读取·密钥创建"| M05
    IS23["I-S23 会话随机量"] -->|"CSPRNG读取·会话签发"| M02
  end

  subgraph LP1["L-P1 公共状态 12"]
    IP01["I-P01 健康请求"] -->|"HTTP·事件"| M07 -->|"HTTP响应·事件"| OP01["O-P01 健康快照"]
    IP02["I-P02 健康详情请求"] -->|"HTTP·事件"| M07 -->|"HTTP响应·事件"| OP02["O-P02 健康详情"]
    IP03["I-P03 存活请求"] -->|"HTTP·事件"| M01 -->|"HTTP响应·事件"| OP03["O-P03 存活状态"]
    IP04["I-P04 就绪请求"] -->|"HTTP·事件"| M07 -->|"HTTP响应·事件"| OP04["O-P04 就绪状态"]
    IP05["I-P05 管理页请求"] -->|"HTTP·事件"| M02 -->|"HTTP响应·事件"| OP05["O-P05 管理页面"]
    IP06["I-P06 根路径请求"] -->|"HTTP·事件"| M02 -->|"HTTP响应·事件"| OP06["O-P06 路径重定向"]
  end

  subgraph LG1["L-G1 模型目录 2"]
    IG01["I-G01 模型目录请求"] -->|"HTTP·事件"| M02 -->|"HTTP响应·事件"| OG01["O-G01 模型目录"]
  end
  subgraph LG2["L-G2 生成协议 12"]
    IG02["I-G02 对话请求"] -->|"HTTP·事件"| M03 -->|"HTTP流·事件"| OG02["O-G02 对话结果"]
    IG03["I-G03 响应请求"] -->|"HTTP·事件"| M03 -->|"HTTP流·事件"| OG03["O-G03 响应结果"]
    IG04["I-G04 消息请求"] -->|"HTTP·事件"| M03 -->|"HTTP流·事件"| OG04["O-G04 消息结果"]
    IG05["I-G05 向量请求"] -->|"HTTP·事件"| M03 -->|"HTTP响应·事件"| OG05["O-G05 向量结果"]
    IG06["I-G06 图像请求"] -->|"HTTP·事件"| M03 -->|"HTTP响应·事件"| OG06["O-G06 图像结果"]
    IG07["I-G07 重排请求"] -->|"HTTP·事件"| M03 -->|"HTTP响应·事件"| OG07["O-G07 重排结果"]
  end
  subgraph LG3["L-G3 协议拒绝 2"]
    IG08["I-G08 非法模型请求"] -->|"HTTP·事件"| M02 -->|"HTTP响应·事件"| OG08["O-G08 模型协议错误"]
  end
  M03 --> M04
  M04 --> M03
  M03 --> M07

  subgraph LA1["L-A1 管理会话 12"]
    IA01["I-A01 登录凭据"] -->|"HTTP·事件"| M02 -->|"HTTP响应·事件"| OA01["O-A01 登录会话"]
    IA02["I-A02 会话查询"] -->|"HTTP·事件"| M02 -->|"HTTP响应·事件"| OA02["O-A02 会话状态"]
    IA03["I-A03 退出命令"] -->|"HTTP·事件"| M02 -->|"HTTP响应·事件"| OA03["O-A03 退出确认"]
    IA04["I-A04 状态查询"] -->|"HTTP·事件"| M05 -->|"HTTP响应·事件"| OA04["O-A04 管理快照"]
    IA05["I-A05 趋势查询"] -->|"HTTP·事件"| M07 -->|"HTTP响应·事件"| OA05["O-A05 趋势数据"]
    IA06["I-A06 适配器查询"] -->|"HTTP·事件"| M06 -->|"HTTP响应·事件"| OA06["O-A06 适配器目录"]
  end
  subgraph LA2["L-A2 密钥控制 12"]
    IA07["I-A07 提示测试"] -->|"HTTP·事件"| M06 -->|"HTTP响应·事件"| OA07["O-A07 测试结果"]
    IA08["I-A08 密钥查询"] -->|"HTTP·事件"| M05 -->|"HTTP响应·事件"| OA08["O-A08 密钥列表"]
    IA09["I-A09 密钥创建"] -->|"HTTP·事件"| M05 -->|"HTTP响应·事件"| OA09["O-A09 新建密钥"]
    IA10["I-A10 密钥更新"] -->|"HTTP·事件"| M05 -->|"HTTP响应·事件"| OA10["O-A10 更新密钥"]
    IA11["I-A11 密钥撤销"] -->|"HTTP·事件"| M05 -->|"HTTP响应·事件"| OA11["O-A11 撤销确认"]
    IA12["I-A12 重载命令"] -->|"HTTP·事件"| M01 -->|"HTTP响应·事件"| OA12["O-A12 重载确认"]
  end
  subgraph LA3["L-A3 OAuth 会话 12"]
    IA13["I-A13 授权启动"] -->|"HTTP·事件"| M06 -->|"HTTP响应·事件"| OA13["O-A13 授权会话"]
    IA14["I-A14 授权回调"] -->|"HTTP·事件"| M06 -->|"HTTP响应·事件"| OA14["O-A14 回调确认"]
    IA15["I-A15 授权轮询"] -->|"HTTP·事件"| M06 -->|"HTTP响应·事件"| OA15["O-A15 授权状态"]
    IA16["I-A16 凭据查询"] -->|"HTTP·事件"| M06 -->|"HTTP响应·事件"| OA16["O-A16 凭据摘要"]
    IA17["I-A17 凭据刷新"] -->|"HTTP·事件"| M06 -->|"HTTP响应·事件"| OA17["O-A17 刷新结果"]
    IA18["I-A18 优先级更新"] -->|"HTTP·事件"| M06 -->|"HTTP响应·事件"| OA18["O-A18 优先级结果"]
  end
  subgraph LA4["L-A4 OAuth 策略 8"]
    IA19["I-A19 凭据删除"] -->|"HTTP·事件"| M06 -->|"HTTP响应·事件"| OA19["O-A19 删除确认"]
    IA20["I-A20 凭据状态更新"] -->|"HTTP·事件"| M06 -->|"HTTP响应·事件"| OA20["O-A20 状态结果"]
    IA21["I-A21 路由策略查询"] -->|"HTTP·事件"| M06 -->|"HTTP响应·事件"| OA21["O-A21 路由策略"]
    IA22["I-A22 路由策略更新"] -->|"HTTP·事件"| M06 -->|"HTTP响应·事件"| OA22["O-A22 策略结果"]
    IA29["I-A29 非法管理请求"] -->|"HTTP·事件"| M02 -->|"HTTP响应·事件"| OA29["O-A29 管理协议错误"]
  end
  subgraph LA5["L-A5 账号路由 12"]
    IA23["I-A23 账号测试"] -->|"HTTP·事件"| M06 -->|"HTTP响应·事件"| OA23["O-A23 账号测试结果"]
    IA24["I-A24 账号导出"] -->|"HTTP·事件"| M05 -->|"HTTP响应·事件"| OA24["O-A24 账号导出包"]
    IA25["I-A25 账号导入"] -->|"HTTP·事件"| M05 -->|"HTTP响应·事件"| OA25["O-A25 账号导入结果"]
    IA26["I-A26 账号保存"] -->|"HTTP·事件"| M05 -->|"HTTP响应·事件"| OA26["O-A26 保存确认"]
    IA27["I-A27 账号删除"] -->|"HTTP·事件"| M05 -->|"HTTP响应·事件"| OA27["O-A27 删除确认"]
    IA28["I-A28 路由保存"] -->|"HTTP·事件"| M05 -->|"HTTP响应·事件"| OA28["O-A28 路由确认"]
  end

  subgraph LR1["L-R1 服务返回 8"]
    IR01["I-R01 模型上游响应"] -->|"HTTP读取·尝试"| M03
    IR02["I-R02 提示上游响应"] -->|"HTTP读取·测试"| M06
    IR03["I-R03 账号目录响应"] -->|"HTTP读取·测试"| M06
    IR04["I-R04 能力目录响应"] -->|"HTTP读取·十分钟"| M06
    IR05["I-R05 适配器根响应"] -->|"HTTP读取·探针"| M06
    IR06["I-R06 适配器模型响应"] -->|"HTTP读取·探针"| M06
    IR07["I-R07 OAuth管理响应"] -->|"HTTP读取·管理操作"| M06
    IR08["I-R08 OAuth模型响应"] -->|"HTTP读取·授权完成"| M06
  end

  subgraph LU1["L-U1 外部调用 10"]
    M03 -->|"HTTP请求·上游尝试"| OU01["O-U01 模型上游请求"]
    M06 -->|"HTTP请求·十分钟"| OU02["O-U02 能力目录请求"]
    M06 -->|"HTTP请求·账号测试"| OU03["O-U03 账号目录请求"]
    M06 -->|"HTTP请求·适配器查询"| OU04["O-U04 Atom根探针"]
    M06 -->|"HTTP请求·适配器查询"| OU05["O-U05 Grok根探针"]
    M06 -->|"HTTP请求·适配器查询"| OU06["O-U06 Gemini根探针"]
    M06 -->|"HTTP请求·适配器查询"| OU07["O-U07 CLI根探针"]
    M06 -->|"HTTP请求·适配器查询"| OU08["O-U08 CLI模型探针"]
    M06 -->|"HTTP请求·管理操作"| OU09["O-U09 OAuth管理请求"]
    M06 -->|"HTTP请求·授权完成"| OU10["O-U10 OAuth模型请求"]
  end
  OU01 -.->|"HTTP返回·模型请求"| IR01
  OU01 -.->|"HTTP返回·提示测试"| IR02
  OU02 -.->|"HTTP返回·十分钟"| IR04
  OU03 -.->|"HTTP返回·账号测试"| IR03
  OU07 -.->|"HTTP返回·根探针"| IR05
  OU08 -.->|"HTTP返回·模型探针"| IR06
  OU09 -.->|"HTTP返回·管理操作"| IR07
  OU10 -.->|"HTTP返回·授权完成"| IR08

  subgraph LS2["L-S2 外部落点 12"]
    M05 -->|"原子文件写·配置变更"| OS01["O-S01 服务配置文件"]
    M05 -->|"原子文件写·密钥变更"| OS02["O-S02 托管密钥文件"]
    M07 -->|"NDJSON写·模型请求"| OS03["O-S03 请求摘要文件"]
    M01 -->|"stdout写·异常"| OS04["O-S04 主函数日志"]
    M01 -->|"stdout写·生命周期"| OS05["O-S05 服务日志"]
    M01 -->|"stdout写·恢复失败"| OS06["O-S06 恢复日志"]
    M02 -->|"stdout写·panic"| OS07["O-S07 panic日志"]
    M07 -->|"stdout写·落盘失败"| OS08["O-S08 摘要写错日志"]
    M06 -->|"stdout写·十分钟"| OS09["O-S09 发现轮次日志"]
    M06 -->|"stdout写·同步事件"| OS10["O-S10 能力同步日志"]
    M01 -->|"stdout写·启动一次"| OS11["O-S11 配置状态日志"]
    M01 -->|"进程退出·终止"| OS12["O-S12 进程终态"]
  end
  subgraph LC1["L-C1 CLI诊断 2"]
    IC01["I-C01 诊断参数"] -->|"进程启动·命令"| M01 -->|"进程返回·命令"| OC01["O-C01 诊断结果"]
  end
```

## 5. 对账

| 类别 | 第 1～3 节条目数 | 第 4 节唯一节点数 | 结果 |
|---|---:|---:|---|
| 外部输入 | 75（12 + 6 + 8 + 29 + 8 + 1 + 11） | 75 | 一致 |
| 外部输出 | 66（6 + 8 + 29 + 10 + 12 + 1） | 66 | 一致 |
| 内部核心模块 | 7 | 7 | 一致 |
| 合计 | 148 | 148 | 一致 |

第二层规模复核：`L-S1=12`、`L-S3=11`、`L-P1=12`、`L-G1=2`、`L-G2=12`、`L-G3=2`、`L-A1=12`、`L-A2=12`、`L-A3=12`、`L-A4=10`、`L-A5=12`、`L-R1=8`、`L-U1=10`、`L-S2=12`、`L-C1=2`；均满足“单层外部输入加外部输出不超过 12”。`CORE=7`，满足内部核心模块不超过 7。

## 6. 运行态差异

- [推断] `systemctl cat lite2api.service` 在审计时警告磁盘单元已变化但 manager 尚未 `daemon-reload`。依据：本次只读命令的直接输出；该警告没有稳定文件行号。因此 `/etc/systemd/system/lite2api.service` 只证明磁盘期望值，不能单独证明已加载值。
- [推断] `systemctl show lite2api.service` 在审计时返回 `ActiveState=active`、`SubState=running`、`MainPID=1093926`、`NRestarts=0`，并显示已加载 `ExecStart=/usr/local/bin/lite2api -config /etc/lite2api/config.json`。依据：本次只读命令的直接输出；PID 属瞬时事实，不写入流程图节点。
- [真实] 生产 Nginx 管理 allowlist 有四个地址，应用配置也有四个 CIDR；Nginx 证据见 `/etc/nginx/sites-available/sub2api.conf:73`～`/etc/nginx/sites-available/sub2api.conf:77`，应用证据见 `/etc/lite2api/config.json:9`～`/etc/lite2api/config.json:14`。
