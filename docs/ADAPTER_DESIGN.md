# Adapter Design

## 设计目标

适配器层解决“认证和上游协议不同”，不是重新建立一套平台。Lite2API 保持单机、单管理员、内存调度；适配器只在原生转发无法覆盖 OAuth、Cookie、设备授权或私有协议时独立运行。设计排序为：正确路由、凭据隔离、故障隔离、低资源、可观测、可替换。

## 类型模型

一个适配器不能只用 `openai` 或 `anthropic` 描述。当前逻辑拆成四个正交维度：

| 维度 | 字段/来源 | 作用 |
|---|---|---|
| 实现类型 | `adapter_id` | 标识 `generic-openai`、`cli-proxy-api`、`grok2api` 等实现 |
| 实例 | `instance_id` | 区分同一实现的 `official`、`local` 或不同区域实例 |
| 请求操作 | `operations` | 在调度前判断是否能处理当前端点 |
| 传输协议 | `type`、`base_url`、认证字段 | 决定 URL、Header、连接池和请求的透明转发方式 |

稳定操作类型只有六个：

- `openai.chat` → `/v1/chat/completions`
- `openai.responses` → `/v1/responses`
- `anthropic.messages` → `/v1/messages`
- `openai.embeddings` → `/v1/embeddings`
- `openai.images` → `/v1/images/*`
- `openai.rerank` → `/v1/rerank`

不要用模型名称猜协议。相同模型可能同时出现在多个 API 形态中，模型匹配只能发生在操作能力过滤之后。旧账号没有 `operations` 时，OpenAI 兼容账号使用旧版宽能力默认值，Anthropic 账号只使用 Messages，以保证无中断升级。

## 适配器选择

1. 官方 API Key 且协议兼容：使用内置适配器，不增加进程。
2. 官方 OpenAI 兼容端点：仍使用内置适配器，用独立 `instance_id` 表示供应商。
3. OAuth、setup-token 或设备授权：使用 `cli-proxy-api` 这类认证池，凭据由外部进程刷新。
4. Cookie、Web 会话、逆向协议：每类供应商一个隔离进程，不把 Cookie 写入 Lite2API 配置。
5. 本地推理：只有真正部署推理引擎后才建账号；目录收录不触发自动安装。
6. 需要协议转换时：新增明确的转换适配器，不在通用调度器中加入按供应商分支。

## 最小契约

每个外置适配器必须满足：

- 只监听 loopback，Lite2API 使用固定 `base_url` 访问。
- 模型面与管理面使用不同密钥；浏览器不能获得任何一把服务端密钥。
- 提供兼容的模型列表和至少一个声明的操作端点。
- 不依赖 Lite2API 请求日志、数据库或注册中心维持状态。
- OAuth/Cookie 凭据在独立的 `0700` 目录中，由适配器自行刷新。
- 版本或提交固定；升级前先测试回环健康、管理鉴权、模型鉴权和真实请求。
- HTTP 超时、连接上限和适配器自身并发必须有界，避免把上游卡死传播到网关。

## 接入交互类型

| 类型 | 管理页交互 | 凭据归属 | 运行策略 |
|---|---|---|---|
| API Key / 兼容 API | 手工填写 Key、Base URL、操作类型 | Lite2API 环境变量或配置 | 内置，无额外进程 |
| OAuth 回调 | 直接生成链接，粘贴 localhost 回调 URL | CLIProxyAPI 隔离认证池 | 共享一个路由档位 |
| 设备授权 | 直接生成链接/设备码，后台轮询完成 | 外部认证池 | 共享一个路由档位 |
| Browser Cookie | 扩展导出，浏览器本地归一化 | 单供应商 Web 适配器 | 配好凭据后按需启动 |
| Web/Console SSO | 扩展或适配器控制台导出文本/JSON | 单供应商适配器 | 配好凭据后按需启动 |

管理页只能展示脱敏身份、就绪状态和请求计数，不能返回 Token、Cookie、凭据路径或原始适配器错误。认证凭据数量与 Lite2API 路由档位数量没有一一对应关系。

## 生命周期状态

目录状态由四个维度合成：

| 状态 | 含义 | 是否承载流量 |
|---|---|---|
| `built-in` | 进程内原生能力 | 是，但仍需账号和密钥 |
| `installed` | 文件/编排已存在，尚未探测 | 否 |
| `stopped` | 回环探针不可达 | 否 |
| `running` | 进程可达，但 Lite2API 尚未配置账号 | 否 |
| `auth-required` | 进程可达，模型列表为空或尚无凭据 | 否 |
| `ready` | 进程、凭据、模型和关联账号均就绪 | 是 |
| `catalog` | 只收录，尚未部署和审计 | 否 |

`install_status` 保留静态安装事实，`runtime_status` 表示进程，`readiness` 表示依赖完整性，`traffic` 是最终流量结论。这样不会再把“代码已安装”显示成“授权可用”。

## 资源预算

普通请求热路径只增加一次内存中的字符串能力匹配，不执行探针、不读取磁盘。管理端 `/adapters` 才探测固定 loopback URL：连接超时 250 ms、响应头超时 300 ms、总超时 500 ms、结果缓存 60 秒、不跟随重定向。单机不引入 Redis、数据库、队列、注册中心或高频健康轮询。

新增适配器应优先设置以下预算：

- 空闲常驻内存：简单代理目标小于 100 MiB；超过时必须说明原因。
- 空闲 CPU：接近 0，不允许秒级轮询或忙等待。
- 文件状态：仅凭据和必要配置，不保存请求/响应正文。
- 网络：只访问上游和 loopback；管理探针不能访问任意 URL。
- 失败域：适配器退出不能导致 Lite2API 退出，调度器会跳过不健康账号。

## 新增实现步骤

1. 在适配器目录登记 ID、固定来源、许可证、认证模式、协议和操作类型。
2. 为账号模板填写 `adapter_id`、`instance_id`、`operations`、回环 URL 和密钥环境变量。
3. 使用独立 systemd 服务或 Compose profile，设置非 root 用户、只读系统、最小写目录和重启边界。
4. 增加按需探针；只允许固定 loopback URL，任何响应体都要限长。
5. 测试每个操作类型的正向路由和不兼容操作的拒绝/换号。
6. 测试凭据为空、进程停止、模型为空、认证失败、429、5xx、流中断和客户端取消。
7. 将安装、版本、配置位置、资源基线和故障判断同步到运维文档。

## 额度窗口契约

- 额度只能来自提供方明确返回的字段或官方接口；本地请求数不能冒充官方剩余额度。
- Claude 采集挂在真实请求响应上；Codex、Gemini CLI 与 Antigravity 只在账号页可见时按需查询官方接口，每个账号至少缓存 10 分钟，不运行常驻轮询。
- 标准窗口为 `kind`、`observed_at`、`source`，并按提供方携带 `used_percentage`、`remaining + unit`、`model`、`status/reset_at` 中可验证的字段；未知值返回空数组。
- 只允许适配器内部白名单解析；禁止为了 UI 开启全局 `passthrough-headers`。
- 额度百分比只做容量提示。只有提供方 429、冷却或现有健康状态机才会把账号移出调度。
- 新渠道先实现解析夹具和脱敏管理测试，再在 UI 声明支持；否则显示“暂未返回可量化额度”。
- Claude 优先使用真实推理响应头；Codex 使用 `/backend-api/wham/usage`；Gemini CLI 与 Antigravity 使用 `v1internal:retrieveUserQuota`。官方查询必须异步并按账号隔离。
- 缺少百分比不能按 0% 序列化或渲染；只有余额时显示余额，只有冷却时只显示状态和重置时间。

只有当上述步骤全部通过，状态才可以从 `catalog`/`installed` 提升到 `ready`。
