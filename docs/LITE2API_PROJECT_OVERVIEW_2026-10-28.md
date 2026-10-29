文首约定：
1. 本文说明 Lite2API 的目标、范围、接口、流程、依赖及运行和安全边界，核查目录为 `/root/Lite2API`。
2. 源码基准为 Commit `9956f7749b39a1fa941a1f4acc2040e87fc51256`，提交说明为 `fix: recover OAuth accounts and harden gateway errors`；核查日期为 2026 年 10 月 28 日，日期按北京时间表述。
3. “已证实”表示当前源码可定位，或本次隔离验证直接支持；“历史记录”表示仓库文档记载；“部分实现”“存在冲突”“待核实”分别保留实现缺口、资料差异和证据空白。推测与设计建议单列，不作为现有能力。
4. 本次完成源码与文档核查、核心回归、构建和示例配置校验。未调用生产模型、修改生产配置、重启服务或执行部署；现有未跟踪案例稿及旧证据文件予以保留。
注：文件链接定位到核查时的源码行；后续文件变化时，应结合上述 Commit 复核。本文及本次验证证据为新增交付，尚未提交 Git。


## 目标、使用者与典型场景

项目目标：
1. 已证实：Lite2API 是面向个人使用、单节点、单管理员的轻量 AI API 网关，将多个上游账号和渠道收敛到统一 API 入口。
    * 它处理接入地址和凭据分散、账号并发竞争、模型路由、部分上游失败及运行状态判断问题。
    * 设计优先级是流式正确性、密钥安全、故障隔离、低延迟和低运维成本；它转发请求，不自行运行模型。
2. 已证实：客户端可通过稳定模型别名调用，由网关选择兼容渠道并改写上游模型 ID；渠道替换主要在网关配置内完成。
3. 已证实：项目通过连接复用、账号池、并发准入和有条件的失败切换提高调用可控性；现有证据不足以给出长期成功率、成本或运维工时的改善比例。
注：依据为 [README.md 第 3 行](/root/Lite2API/README.md:3)、[架构目标与请求路径](/root/Lite2API/docs/ARCHITECTURE.md:3)、[模型映射与调度](/root/Lite2API/internal/gateway/runtime.go:678)。

使用者与典型场景：
1. 已证实的目标使用者：能维护配置、凭据和单机服务的个人开发者或自托管管理员。
    * 个人开发、脚本及兼容客户端使用客户端 Key 调用；管理员维护渠道、别名、路由和访问策略。
    * 多个客户端 Key 表示多个受控访问凭据，不构成多租户用户系统。
2. 典型场景：在多个兼容 API 账号间分配流量，限制每个账号的并发，在明确拒绝执行时尝试备用目标。
3. 典型场景：通过本机 CLIProxyAPI 管理 OAuth 凭据池，或通过隔离的 Web 适配器接入 Cookie/SSO 渠道；客户端继续使用网关入口。
4. 典型场景：在管理页创建临时或服务用 Key、检查渠道授权状态、查看最近请求摘要，以及迁移受支持的 Sub2API 账号数据。
注：依据为 [产品边界](/root/Lite2API/README.md:147)、[账号接入说明](/root/Lite2API/README.md:83)、[客户端 Key 策略](/root/Lite2API/internal/gateway/clientkeys.go:172)。场景是现有功能的使用方式，不代表已有用户规模或使用频率。


## 项目范围与非目标

职责范围：
1. 网关核心负责客户端鉴权、访问策略、路由与模型改写、账号级连接池、并发控制、故障判断、流式转发、管理接口和摘要观测。
2. 外置适配器负责各自的认证和协议细节。CLIProxyAPI 承担受支持的 OAuth 凭据保存、刷新及内部选号；Grok2API、Gemini Web 等在独立进程或容器内运行。
    * 一个 Lite2API 路由连接可以对应多个适配器内部凭据。连接数量与认证账号数量不是同一指标。
    * 第三方能力的可用性依赖对应适配器及有效凭据，不能全部归为核心二进制内置功能。
3. 核心以 Go 标准库独立实现，不以 Sub2API 为运行依赖；Sub2API 在这里主要是兼容接口和迁移数据的来源。
注：依据为 [渠道职责](/root/Lite2API/channels/README.md:3)、[两层账号池](/root/Lite2API/channels/README.md:79)、[核心模块声明](/root/Lite2API/go.mod:1)。

非目标：
1. 不提供终端用户注册、余额、套餐、支付、订阅、多租户隔离或团队角色权限。渠道 OAuth 授权不是网关终端用户登录。
2. 不提供多节点共享的 RPM、并发租约、撤销事件或一致性存储；当前限制和运行状态主要在单进程内生效。
3. 不承担任意协议之间的通用转换。核心允许模型及少量兼容字段改写，但不会普遍将 OpenAI Chat 转换为 Anthropic Messages；特定渠道转换可由适配器实现。
4. 不保证任意模型都支持所有端点，也不保证上游授权、额度、模型输出质量或持续可用性。目录收录不代表已经安装、授权或通过真实推理验证。
注：依据为 [有意不实现的能力](/root/Lite2API/docs/ARCHITECTURE.md:95)、[端点操作映射](/root/Lite2API/internal/gateway/gateway.go:1131)、[适配器目录说明](/root/Lite2API/README.md:155)。


## 核心功能和对外接口

模型接口：
1. `GET /v1/models`：返回配置和启用渠道汇总的模型目录，并按托管 Key 的模型权限过滤；其中包含稳定路由别名和符合条件的逻辑模型或上游模型。
2. `POST /v1/chat/completions` 与 `POST /v1/responses`：转发对应 OpenAI 风格 JSON 请求，支持流式及非流式响应。
3. `POST /v1/messages` 与 `POST /v1/messages/count_tokens`：转发 Anthropic Messages 及计数请求；网关自身错误采用相应协议格式。
4. `POST /v1/embeddings`：转发向量生成请求。
5. `POST /v1/images/generations`：转发图片生成请求。不能由此推定支持图片编辑、变体或任意文件上传。
6. `POST /v1/rerank`：转发重排请求；必须存在支持该操作的渠道和模型。
7. 上述接口接受 `Authorization: Bearer` 或 `X-Api-Key`。POST 数据面要求有效 JSON 对象和非空 `model`；未支持的路径、错误方法、非法请求及权限不足会被拒绝。
注：接口存在已由源码证实，具体模型的端到端可用性需另行验证。依据为 [入口鉴权及方法校验](/root/Lite2API/internal/gateway/gateway.go:359)、[模型目录输出](/root/Lite2API/internal/gateway/gateway.go:1120)、[完整端点映射](/root/Lite2API/internal/gateway/gateway.go:1131)、[认证头读取](/root/Lite2API/internal/gateway/adminauth.go:259)。

关键能力：
1. 路由与兼容性筛选：按操作类型、逻辑模型、推理强度、账号启用状态和健康状况选择目标，再解析为真实上游模型；不会仅凭相同模型名认定两个渠道协议兼容。
    * 显式 `targets[]` 默认按数组顺序，也可选择最少负载、轮询、账号优先级或会话粘性。旧式路由默认采用最少负载。
    * 外层账号优先级数值越小越优先；CLIProxyAPI 内部凭据优先级数值越大越优先，两层含义不能混用。
2. 连接与并发：每个账号独享 HTTP Transport，复用 TCP/TLS 连接并尝试 HTTP/2；使用原子并发槽。流式调用在传输结束后释放 Key 和账号槽，热加载时保留在途容量计数。
3. 故障处理：区分无兼容候选、账号不可用、容量占满及提交结果不确定；仅容量阻塞进入有界等待，静态无候选直接失败。
4. 客户端 Key：托管 Key 可配置模型白名单、RPM、并发和到期时间，可禁用或撤销；创建时只返回一次完整明文，持久化保存摘要。请求路径使用内存快照和原子计数。
5. 账号维护：支持手动接入、快捷 OAuth、测试、删除、批量导入和导出；导入具备预检查及逐项错误结果，单次最多 500 个账号。
6. 热加载：管理端配置更新、`SIGHUP` 或 reload API 可切换运行快照。新状态构建失败时保持旧运行状态，部分服务器字段必须重启。
7. 摘要观测：展示模型、渠道、操作、耗时、结果、模态及上游 usage 等信息；最近请求环保留 512 条，另有有界摘要文件和趋势数据。未知 usage 或额度不能视为零。
注：依据为 [调度实现](/root/Lite2API/internal/gateway/runtime.go:637)、[调度设计与两层优先级](/root/Lite2API/docs/ARCHITECTURE.md:30)、[连接池](/root/Lite2API/internal/gateway/gateway.go:326)、[Key 创建](/root/Lite2API/internal/gateway/clientkeys.go:273)、[导入上限](/root/Lite2API/internal/gateway/accountimport.go:18)、[状态提交](/root/Lite2API/internal/gateway/gateway.go:143)、[摘要字段](/root/Lite2API/internal/gateway/stats.go:17)。

管理接口：
1. 页面为 `/admin`；管理 API 前缀为 `/admin/api`，先校验来源 CIDR，再执行相应登录或鉴权逻辑。
2. 会话接口：`POST /login`、`GET /session`、`POST /logout`；日常状态接口：`GET /state`、`GET /trends`、`GET /adapters`。
3. Key 接口：`GET/POST /client-keys`、`PUT/DELETE /client-keys/:id`；账号连接接口：`PUT /accounts`、`DELETE /accounts/:id`、`POST /accounts/test`。
4. 配置和数据接口：`PUT /routes`、`POST /reload`、`POST /accounts/import`、`POST /accounts/export`。`PUT /routes` 替换整个路由集合，调用者需保留仍然需要的条目。
5. OAuth 接口：`POST /oauth/start`、`POST /oauth/callback`、`POST /oauth/status`、`GET/DELETE /oauth/accounts`；另提供账号刷新、优先级、状态和 `GET/PUT /oauth/routing`。
6. `POST /prompt-test` 用于指定账号的提示测试，会调用上游；它不是仅检查连通性的免费健康接口。
注：本块除页面外的路径均相对于 `/admin/api`。完整方法和分发以 [admin.go 第 19 行](/root/Lite2API/internal/gateway/admin.go:19)为准；[路由集合替换](/root/Lite2API/internal/gateway/admin.go:353)、[提示测试](/root/Lite2API/internal/gateway/prompttest.go:36)说明对应行为。

健康与就绪接口：
1. `GET /livez`：只表明进程能够处理存活检查，返回 `status=ok`。
2. `GET /health` 与 `/health/details`：将 `liveness=ok` 与路由状态 `unknown/ready/degraded/unavailable` 分开；状态为 `unavailable` 时返回 503，其余状态可返回 200。
3. `GET /readyz`：额外检查各配置路由的结构可用性、最新有效结果及五分钟新鲜度；已有失败、过期观测或无结构可用目标可导致 503。冷启动时结构可用但尚无观测，可以返回 200 和 `unknown`。
注：这些 handler 未要求客户端 Key，反向代理是否暴露它们取决于部署配置。200 或模型目录可见均不足以证明所有路由可推理；当前观测也未覆盖每个“模型 × 操作 × 特性”组合。依据为 [服务路由注册](/root/Lite2API/internal/gateway/server.go:27)、[readiness 判定](/root/Lite2API/internal/gateway/server.go:90)、[health 判定](/root/Lite2API/internal/gateway/server.go:174)。


## 业务流程与成功判据

接入与调用流程：
1. 准备网关配置和独立密钥 → 校验配置 → 启动核心及所需适配器 → 配置有效上游与模型路由。
    * 示例上游初始禁用，能启动不代表已有推理能力。对外访问通过 HTTPS 反向代理，管理入口限制来源。
2. 管理员在允许的网络登录 → 创建托管客户端 Key → 保存一次性明文 → 为客户端设置 Base URL 和模型别名。
    * 先以 `/v1/models` 检查客户端鉴权与目录，再对需要的模型和端点进行最小真实请求验证。
3. 客户端请求 → Key 鉴权及策略检查 → 入口并发与字节预算准入 → 有界读取和解析 JSON → 提取模型与会话 → 筛选并获取账号槽 → 改写必要字段并发送上游 → 转发响应 → 释放资源并记录摘要。
    * 请求体先在网关内有界缓冲，不是任意请求体的零缓存透传；响应可流式转发。
4. OAuth 接入 → 生成授权链接或设备码 → 用户完成平台授权 → 提交回调并查询状态 → 适配器保存凭据 → 网关接入共享账号池。
    * 回调成功和凭据加载成功只是接入步骤，仍需真实调用判断授权、模型及额度是否可用。
5. 修改配置 → 验证候选配置并构建运行资源 → 原子保存和切换快照。发现磁盘配置与当前状态冲突时拒绝更新，需先 reload 后重试。
注：依据为 [示例账号初始状态](/root/Lite2API/config.example.json:29)、[数据面处理](/root/Lite2API/internal/gateway/gateway.go:359)、[OAuth 接入说明](/root/Lite2API/README.md:83)、[配置事务](/root/Lite2API/internal/gateway/admin.go:363)。

失败处理流程：
1. 已证实：可证明未写入上游的连接类失败，以及明确拒绝的 401/402/403/429，可尝试其他目标；显式目标返回模型 404 也可继续目标链。
2. 已证实：普通上游 POST 收到 408/409/425/5xx，或写入后发生超时、断连，返回 502 `uncertain_submission`，停止跨账号自动重放；Embeddings 和 Rerank 也遵守这一规则。
    * 受信本机 CLIProxyAPI 明确提供“未提交”证明时存在受限的安全切换例外；客户端伪造的内部标记会被剥离。
3. 已证实：匹配受信本机 OAuth 适配器的明确 401，可合并同凭据刷新，刷新成功后最多重试一次；刷新结果缓存一分钟，单次刷新上下文上限 12 秒，协调器最多 128 项。
    * 上游授权失败最终归一为 502 `upstream_auth_failed`；Messages 使用 Anthropic 错误信封。客户端 Key 无效仍为 401。永久失效的 refresh token 需要用户重新授权。
4. 已证实：流已开始后中断不换号拼接；客户端取消向上游传递，客户端或下游故障不应作为上游账号故障惩罚。
注：依据为 [重试分支和受信证明](/root/Lite2API/internal/gateway/gateway.go:650)、[提交确定性策略](/root/Lite2API/internal/gateway/gateway.go:1731)、[OAuth 刷新及错误归一](/root/Lite2API/internal/gateway/oauthrecovery.go:89)、[流式结束处理](/root/Lite2API/internal/gateway/gateway.go:771)。

成功判据：
1. 接入成功：预期渠道和凭据已保存且可被路由选中；以目标模型的真实响应或完整流验证推理可用，不能只验收健康页或目录。
2. 调用成功：业务验收应确认目标协议的有效结果及完整响应；只收到 HTTP 200 响应头不足以通过。
    * 核心统计依据 HTTP 状态、传输结果和 `Outcome`，不独立判断回答质量；正常 EOF 也不等于已验证协议终止事件，因此业务验收还需由客户端或测试检查结果内容。
3. 调度正确：静态不兼容不耗尽队列等待时间；账号满载等待有界；明确拒绝可安全切换；不确定提交不会自动请求备用上游。
4. 边界生效：无效 Key 被拒绝，模型、RPM、并发及有效期策略按配置执行；不允许的管理来源和缺少 CSRF 的 Cookie 会话写请求被拒绝。
5. 配置更新正确：磁盘配置与运行快照一致，验证失败不发布新状态，在途流和容量计数不因热加载消失。
6. 运行可诊断：流中断计为失败，未观察到的能力保持未知，摘要和持久化状态可解释其保留范围；导入需核对逐项结果，不能只看 HTTP 200。
注：这些是依据现有行为形成的验收口径。源码和回归可验证逻辑判据；输出质量、长期可用率、性能目标及真实凭据有效性需要单独验收。


## 技术依赖与约束

技术依赖：
1. 核心运行产物是包含管理页面的 Go 二进制，无第三方 Go 模块运行依赖，不要求 PostgreSQL、Redis 或 Node.js。
    * `go.mod` 声明语言版本为 Go 1.23；当前 Docker 构建和运维安装流程固定使用 Go 1.26.5。语言版本声明与发布工具链不是同一个约束。
    * 本次依赖枚举中，非标准库包均属于 Lite2API 自身。Node 用于前端逻辑测试，不是核心服务运行条件。
2. 完整渠道环境会增加外部依赖：CLIProxyAPI、Grok2API、Gemini Web、相应运行时及第三方平台。核心的“无第三方 Go 依赖”不适用于整套适配器系统。
3. 仓库支持 Docker Compose 和 systemd。Compose 主服务使用 Linux host network；systemd 安装、Nginx 配置及 Shell 运维流程也面向 Linux。
4. 核心必须能写入配置、客户端 Key 文件和摘要日志。OAuth 凭据单独保存在适配器目录；Docker 卷、环境文件及适配器状态都属于备份范围。
注：依据为 [go.mod](/root/Lite2API/go.mod:1)、[核心 Dockerfile](/root/Lite2API/Dockerfile:1)、[Compose](/root/Lite2API/docker-compose.yml:1)、[固定构建与安装](/root/Lite2API/docs/OPERATIONS.md:36)、[备份范围](/root/Lite2API/docs/OPERATIONS.md:80)。

运行与资源约束：
1. 源码默认监听 `127.0.0.1:45679`。源码默认单请求体上限 64 MiB、入口并发 256；示例配置及当前 Compose 默认覆盖为 8 MiB 和 32，不能混称为同一组默认值。
2. 常用默认超时为请求读取 30 秒、容量排队 30 秒、上游响应头 5 分钟、流空闲 15 分钟；流空闲超时不等同于请求总时长限制。
3. 请求读取、JSON 副本和模型改写计入独立字节预算。超限拒绝，不意味着宿主机或适配器的全部内存都由此预算覆盖。
4. 示例 Compose 对核心设置 256 MiB 内存限制、128 个进程/线程任务上限、只读根文件系统和受限写卷；systemd unit 使用 192M 内存上限。它们是部署配置，不能当作实际峰值或吞吐承诺。
5. 摘要文件默认单文件 8 MiB，加两个备份，总量约 24 MiB。异步队列容量 256，队列满可丢弃摘要并累计 `dropped`，不能用作完整账本。
6. 进程累计计数、Key 限流计数及会话等内存状态重启后重建；部分请求摘要、路由证据和趋势可从有界日志恢复，不应笼统称为“全部统计永久保存”或“全部统计都会丢失”。
7. 适配器目录探针按需运行，具备并发和时间预算；另有模型发现任务在启动约两秒后执行，并每十分钟运行，可更新持久化配置。两种机制不能合并描述为“系统没有后台轮询”。
注：依据为 [源码默认值](/root/Lite2API/internal/config/config.go:136)、[示例配置](/root/Lite2API/config.example.json:3)、[Compose 资源与覆盖值](/root/Lite2API/docker-compose.yml:8)、[systemd unit](/root/Lite2API/deploy/lite2api.service:6)、[字节预算](/root/Lite2API/internal/gateway/gateway.go:415)、[日志队列](/root/Lite2API/internal/gateway/requestlog.go:269)、[启动恢复](/root/Lite2API/internal/gateway/gateway.go:76)、[模型发现任务](/root/Lite2API/internal/gateway/capabilitysync.go:19)。

安全边界：
1. 客户端 Key、管理员 Token、适配器调用 Key 和适配器管理 Key 职责分离。模型调用没有有效 Key 时拒绝；只有管理员凭据不能替代客户端 Key。
2. 管理面先校验来源 CIDR；只有来自配置中可信代理的请求才采信 `X-Real-IP`。管理员 Bearer 凭据或 Cookie 会话分别完成应用鉴权。
    * 默认关闭自动登录。显式开启 `admin_auto_login` 后，允许的网络来源可以直接获得管理会话，安全性依赖该网络边界；当前登录分支不要求同时提供管理员 Token。
    * Cookie 为 HttpOnly、SameSite=Strict，Secure 标志取决于直接 TLS 或可信代理报告的 HTTPS；Cookie 会话非安全方法要求 `X-CSRF-Token`。管理员 Bearer 请求不要求 CSRF。
3. 普通 Token 登录具备按 IP 失败锁定。管理 Token、CIDR 等认证边界变更会撤销旧 Cookie 会话；管理者本身拥有配置上游和导出数据的高权限。
4. 托管 Key 文件保存 SHA-256 摘要；配置原子写入并设置 `0600`。文件权限不等于加密，上游密钥应优先从环境或受限 EnvironmentFile 注入。
5. 普通状态输出会遮蔽部分密钥字段，但创建客户端 Key 会返回一次明文，账号导出会包含配置内的凭据、自定义请求头及所选代理认证信息。
    * 环境变量型上游 Key 导出保留变量引用，不自动导出对应环境值；因此账号导出不是完整环境备份。
    * OAuth 迁移导入时，核心会接收并处理原始 Token 包，再通过回环管理接口上传给适配器；不能宣称核心在任何路径都不接触 OAuth Token。
6. 入站客户端认证、Cookie、内部选号和安全重试标记等不原样转发；上游重定向被禁用，`Set-Cookie` 不回传。摘要不保存原始提示词、图片或完整响应正文，但模型名、账号标识和错误摘要仍属于运行数据。
7. 普通 HTTP 上游允许 loopback，私网地址或单标签内部主机名需显式开启开关；这是配置校验规则，不能扩大解释为完整出站隔离。OAuth 管理 URL 另有严格的回环 IP 与 HTTP 校验。
8. 核心直接运行 HTTP 服务，公网 TLS 终止和额外来源限制由 Nginx 等部署层承担；容器和 systemd 的非 root、权限清空与资源限制同样依赖实际采用的部署方式。
注：依据为 [客户端鉴权](/root/Lite2API/internal/gateway/clientkeys.go:172)、[来源与 Cookie](/root/Lite2API/internal/gateway/adminauth.go:325)、[自动登录](/root/Lite2API/internal/gateway/admin.go:231)、[CSRF 条件](/root/Lite2API/internal/gateway/adminauth.go:282)、[配置落盘](/root/Lite2API/internal/config/config.go:906)、[凭据导出](/root/Lite2API/internal/gateway/accountexport.go:64)、[OAuth 导入](/root/Lite2API/internal/gateway/oauthimport.go:12)、[Header 过滤](/root/Lite2API/internal/gateway/gateway.go:1448)、[上游 URL 校验](/root/Lite2API/internal/config/config.go:798)、[OAuth 管理 URL 校验](/root/Lite2API/internal/gateway/oauthadapter.go:710)。


## 已知限制、风险和待核实问题

已证实或部分实现的限制：
1. 单机内存策略无法直接提供多实例全局限流和撤销一致性。单节点失效、宿主机资源耗尽及适配器故障仍是部署层边界。
2. 旧配置或环境变量客户端 Key 被识别为 `legacy-env`，未经过托管 Key 的逐 Key RPM、并发、有效期和模型白名单检查；入口总并发仍生效。
    * 托管 Key 的服务端创建逻辑也允许 RPM/并发为 0、到期时间为空及空模型集合；界面安全预设不等于 API 强制有限权限。
3. 能力描述部分实现：操作能力在账号层声明，模型能力包含逻辑模型、上游 ID 和推理强度，但没有完整的逐模型操作及特性矩阵。旧 OpenAI 类型账号省略 `operations` 时会默认声明多种操作，实际仍可能不支持。
4. 熔断部分实现：已具备失败累计、冷却及恢复资格，但当前通过冷却时间判断可用，尚无完整的 half-open 单探针状态机；冷却后可能有多个请求同时尝试恢复。
5. 流式透传受到上游实现限制。Gemini Web 的精简部署文档明确说明先完整获取结果再输出 SSE，不能据统一接口推定所有渠道都具有逐 Token 实时流式特性。
6. 批量导入允许部分成功；OAuth 凭据上传和网关连接配置不在同一个跨进程事务中，可能出现凭据已保存、连接更新失败的状态，需核对 `errors` 和逐项计数。
7. 摘要有轮转和丢弃边界，缺少原始正文，也未覆盖所有早期鉴权或准入拒绝，不适合作为计费账本、完整审计留存或内容追溯系统；已有防重复重放策略也不构成跨平台 exactly-once 保证。
8. 核心主要验证传输是否结束，正常 EOF 会被当作传输成功，不统一强制检查各协议的完成事件。大于 1 MiB 的请求会跳过详细模态检查并以文本摘要代替，监控模态也不是完整内容识别结论。
注：依据为 [legacy Key 分支](/root/Lite2API/internal/gateway/clientkeys.go:201)、[空白名单与无限并发语义](/root/Lite2API/internal/gateway/clientkeys.go:208)、[Key 参数校验](/root/Lite2API/internal/gateway/clientkeys.go:431)、[能力结构](/root/Lite2API/internal/config/config.go:72)、[默认操作](/root/Lite2API/internal/config/operations.go:24)、[熔断可用判断](/root/Lite2API/internal/gateway/runtime.go:248)、[Gemini 流式说明](/root/Lite2API/channels/README.md:66)、[导入的跨进程边界](/root/Lite2API/internal/gateway/accountimport.go:120)、[摘要记录起点与模态检查](/root/Lite2API/internal/gateway/gateway.go:516)、[EOF 处理](/root/Lite2API/internal/gateway/gateway.go:1561)。

存在冲突的资料：
1. README 中“所有写操作要求 CSRF”应限定为 Cookie 会话；源码明确豁免管理员 Bearer。README 中“未配置管理 Token 即关闭管理 API”也需结合显式自动登录分支理解。
2. 文档对 Secure Cookie 的描述需要前提：源码依据请求是否为 HTTPS 设置该标志，本地 HTTP 不会自动得到 Secure Cookie。
3. CLIProxyAPI 的 Dockerfile 默认版本与恢复文档是 `v6.10.9-lite2api.9`，Compose 仍覆盖 `VERSION` 和镜像标签为 `.6`，构建日期为 2026-08-24。
    * 当前 Dockerfile 已包含五组补丁，不能仅凭 `.6` 标签断言缺少新补丁；但也不能据新文档认定 Compose 产物版本元数据已同步。
4. 较早运维文档仍提“三组补丁”，当前构建定义包含五组。旧案例验证基于 `32c2604`，本次基准为 `9956f77`，旧通过数量不作为当前验证结果。
注：对应来源为 [README 安全说明](/root/Lite2API/README.md:136)、[CSRF 实现](/root/Lite2API/internal/gateway/adminauth.go:282)、[Cookie 实现](/root/Lite2API/internal/gateway/adminauth.go:360)、[Compose 版本覆盖](/root/Lite2API/compose.channels.yml:4)、[当前 Dockerfile 补丁与版本](/root/Lite2API/channels/cliproxyapi/Dockerfile:14)、[旧运维补丁描述](/root/Lite2API/docs/OPERATIONS.md:51)。本次仅记录差异，未修改代码或部署定义。

推测：
1. 统一入口、稳定别名和集中管理有望减少个人开发者切换渠道及排查配置的工作量；尚无本项目接入耗时或排障工时的对照数据。
2. 少依赖、资源限额和有界日志有望降低单机维护复杂度；未有相同负载、相同组件范围的资源对照，不能换算为内存下降比例或成本节省。

设计建议：
1. 将 legacy Key 迁移为托管 Key；如需要应急凭据，应显式定义权限、有效期及审计方式，而非依赖隐含无限策略。
2. 为能力描述补充逐模型操作与特性，并将就绪验收扩展到实际使用的“模型 × 端点 × 流式或其他特性”组合。
3. 补齐 half-open 单探针恢复机制，统一 Compose、Dockerfile、安装器和文档的版本与补丁清单。
4. 区分管理员手写配置与模型发现观测的来源和生命周期；对关键配置、Key 元数据和适配器凭据定期进行隔离恢复演练。
注：本块为建议，未作为现有功能计入交付。

待核实问题：
1. 当前运行二进制、适配器版本及实际生产配置是否与本文 Commit 一致；本次没有读取生产配置或查询线上服务。
2. 所需模型在各端点、客户端版本和流式模式下是否都可用；未重新授权账号恢复后的真实调用结果也需单独确认。
3. 目标负载下的吞吐、P95/P99、峰值内存、成功率和日志丢弃率；本次单元回归及构建不构成压力测试或长期 SLO。
4. 真实备份的跨文件一致性、异地保留及恢复时长；已有备份与回滚脚本不等于完成灾备演练。
5. 外置适配器的全部依赖、维护补丁与真实平台行为；本次没有重建适配器或重跑其完整测试。


## 证据

版本与原始材料：
1. 本次核心源码 Commit：`9956f7749b39a1fa941a1f4acc2040e87fc51256`，提交时间 2026-10-28T21:12:14+08:00。核查和验证期间已跟踪源码无差异。
2. 项目定位与范围由 [README.md](/root/Lite2API/README.md:3)、[ARCHITECTURE.md](/root/Lite2API/docs/ARCHITECTURE.md:3)、[channels/README.md](/root/Lite2API/channels/README.md:3)支持；接口、默认值及安全例外以正文所引当前源码为准。
3. CLIProxyAPI 固定上游 Commit 为 `785b00c3127eea6aa207f1207ead8a2aa93690a3`，实际集成还依赖仓库维护补丁；Gemini Web 的 Git 子模块记录为 `6824ccaaa65768d9a4befc33a417e268f134b252`。子模块版本可通过 `git ls-tree HEAD third_party/cliproxyapi third_party/gemini-web2api` 复核。
4. 本次验证清单及完整通过事件名称保存在 [validation.json](/root/Lite2API/docs/evidence/lite2api-overview-2026-10-28-validation.json)；其中记录命令、源码基准、包结果、环境限制、验证边界及原始输出 SHA-256。

本次实际验证：
1. 工具链：Go 1.26.5、linux/amd64；Node v18.19.1。
2. 执行 `GOTOOLCHAIN=local go test -race ./... -count=1 -json`，退出码 0。
    * `internal/config`、`internal/gateway`、`internal/web` 均通过，`cmd/lite2api` 无测试文件；含子测试共 257 个通过事件，无失败事件。
    * 首次受限执行因 `httptest` 创建临时 IPv6 回环监听被沙箱拒绝而中断；获准放开本地测试端口限制后，完整重跑通过。首次中断不记为产品缺陷，也未隐藏该执行限制。
3. 执行 `node --test internal/web/app-core.test.js internal/web/app-runtime.test.js`，两个测试文件通过，退出码 0。
4. 使用 `GOTOOLCHAIN=local CGO_ENABLED=0 go build -trimpath` 构建核心成功；运行产物以 `-check-config -config config.example.json` 校验成功，输出为 `configuration ok: 4 accounts, 3 routes`。
5. `go list -deps` 核查显示核心入口的非标准库包均为项目自身包；这只覆盖核心，不覆盖外置适配器依赖。
注：原始输出见 [Go JSONL](/root/Lite2API/docs/evidence/lite2api-overview-2026-10-28-go.jsonl)、[Node TAP](/root/Lite2API/docs/evidence/lite2api-overview-2026-10-28-node.tap)。本次未新增产品测试，复用了仓库已有回归。

关键行为与测试定位：
1. 操作筛选和静态不兼容快速失败：[runtime_test.go 第 356 行](/root/Lite2API/internal/gateway/runtime_test.go:356)、[第 373 行](/root/Lite2API/internal/gateway/runtime_test.go:373)；逻辑模型按真实渠道解析：[第 540 行](/root/Lite2API/internal/gateway/runtime_test.go:540)。
2. 明确拒绝后切换及不确定提交不重放：[gateway_test.go 第 185 行](/root/Lite2API/internal/gateway/gateway_test.go:185)、[第 218 行](/root/Lite2API/internal/gateway/gateway_test.go:218)；受信未提交证明：[第 1332 行](/root/Lite2API/internal/gateway/gateway_test.go:1332)。
3. Key 并发或字节预算拒绝时不读请求体：[gateway_test.go 第 468 行](/root/Lite2API/internal/gateway/gateway_test.go:468)、[第 494 行](/root/Lite2API/internal/gateway/gateway_test.go:494)；流式持有账号槽：[第 1586 行](/root/Lite2API/internal/gateway/gateway_test.go:1586)。
4. OAuth 明确 401 刷新及最多一次重试、并发合并、不重放模糊失败：[oauthrecovery_test.go 第 18 行](/root/Lite2API/internal/gateway/oauthrecovery_test.go:18)、[第 64 行](/root/Lite2API/internal/gateway/oauthrecovery_test.go:64)、[第 97 行](/root/Lite2API/internal/gateway/oauthrecovery_test.go:97)。
5. 管理来源防伪造、Cookie CSRF 和显式自动登录：[adminauth_test.go 第 27 行](/root/Lite2API/internal/gateway/adminauth_test.go:27)、[第 48 行](/root/Lite2API/internal/gateway/adminauth_test.go:48)、[第 75 行](/root/Lite2API/internal/gateway/adminauth_test.go:75)。
6. 存活与就绪分离、低流量路由证据保留：[operations_test.go 第 119 行](/root/Lite2API/internal/gateway/operations_test.go:119)、[第 153 行](/root/Lite2API/internal/gateway/operations_test.go:153)；旧运行请求不能污染替换路由：[gateway_test.go 第 1078 行](/root/Lite2API/internal/gateway/gateway_test.go:1078)。
注：上述用例均包含在本次通过记录中。回归证明相应夹具下的行为，不等于真实渠道全部可用或长期运行无故障。

历史实测与适用范围：
1. [2026 年 8 月接口审计](/root/Lite2API/docs/INTERFACE_AUDIT_2026-08-14.md:9)记载 Chat、Responses、Embeddings、Images 真实成功，也记录了凭据失效和无候选等失败。它是历史快照，账号和模型数量不能作为当前状态。
2. [2026 年 10 月恢复验收](/root/Lite2API/docs/OAUTH_RECOVERY_2026-10-28.md:31)记载 Gemini Chat，以及 Shadow 的 Chat、Responses、Messages 和完整流式请求通过；同一记录仍保留部分 GPT/Codex、Claude 和 Antigravity 账号需要重新授权的限制。
3. 恢复记录中的带凭据调用只经本机回环入口，公网只验证了 TLS 可达及无 Key 返回 401；没有单独验证公网带 Key 的端到端请求。该记录对应其发布产物，不直接证明本文 HEAD 已部署。
注：历史记录支持“曾在所述环境成功”，本次回归支持“当前核心实现通过所列隔离用例”；二者均不替代对当前生产部署、全部账号和目标业务负载的重新验收。
