# Architecture

## 目标

Lite2API 是单节点、单管理员的个人 AI 网关。设计优先级依次是：流式正确性、密钥安全、故障隔离、低延迟、低运维成本。

## 请求路径

```text
Client
  -> O(1) API key snapshot lookup + per-key RPM/concurrency/model policy
  -> global in-flight guard
  -> bounded JSON body + model/session extraction
  -> route lookup
  -> operation capability filtering
  -> ordered target compatibility/health filtering
  -> atomic account concurrency acquire
  -> account-isolated HTTP connection pool
  -> upstream
  -> streaming copy with idle watchdog
  -> release account concurrency
```

调度和运行配置封装在不可变的 `runtimeState` 中，通过原子指针整体切换。热加载不会让请求看到一半新、一半旧的配置。

请求路径先映射为稳定的操作类型：`openai.chat`、`openai.responses`、`anthropic.messages`、`openai.embeddings`、`openai.images` 或 `openai.rerank`。账号通过 `adapter_id`、`instance_id` 和 `operations` 描述适配器实例及能力；调度器只在支持当前操作的账号中做模型、健康、并发和策略筛选。旧配置没有 `operations` 时按账号协议自动补齐，保持兼容。

客户端 Key 同样使用不可变 map 快照。管理端创建、更新或撤销时先以 `0600` 原子落盘，再切换指针；请求线程只做 ID map 查找、SHA-256 常量时间比较和原子计数。流式请求直到响应结束才释放 Key 与账号并发槽。最近请求使用固定环形缓冲区，写入为 O(1)。

## 调度

推荐路由在顶层声明逻辑 `model` 与 `reasoning_effort`，有序 `targets[]` 只包含真实渠道。渠道账号通过 `capabilities[]` 把逻辑组合映射到自身的上游模型 ID，因此同一逻辑模型可以由 Antigravity、Claude Code 官方、Web 代理等不同凭据来源实现。调度器先排除不支持该模型或强度的渠道，再严格按数组顺序选择首个健康且有容量的目标；失败后只排除当前渠道并继续。显式目标链会完整执行，不受旧的账号数或 `max_failover_attempts` 静默截断。

只有没有 `capabilities[]` 的旧账号才使用目标级 `model` / `reasoning_effort`。新结构把意图与实现分开：用户调整推理强度后，候选渠道集合会随能力矩阵变化，不需要手工填写或理解渠道专用模型 ID。

以下策略仅用于兼容没有 `targets[]` 的旧路由：

- `least_loaded`：比较 `active / concurrency / weight`，同负载时按 priority 和 ID。
- `round_robin`：原子计数轮询候选账号。
- `priority`：低 priority 优先；满载或失败时自动使用下一个账号。
- `sticky`：使用请求中的 session、conversation、prompt cache key 等做 Rendezvous Hash，尽量维持提示缓存和多轮会话稳定。

每个账号使用 CAS 原子计数获取槽位。热加载时新旧账号对象共享运行计数，因此正在进行的流式请求不会因为配置重载而“消失”。

## 故障模型

- DNS、连接、TLS、响应头错误：失败换号。
- 401/403：立即短期熔断该账号并换号。
- 429：遵守秒数形式的 `Retry-After`，立即熔断并换号。
- 408/409/5xx：计入连续失败；达到阈值后熔断。
- 显式目标返回 404（目标模型不存在）：尝试下一个目标，不把普通客户端 4xx 扩散到全部渠道。
- 已经向客户端发出成功响应头后的流中断：记录错误和账号健康，不尝试换号，避免拼接两个上游流。
- 客户端主动取消：立即传递给上游，不惩罚账号。

## 连接模型

每个账号独占一个 `http.Transport`：

- HTTP/2 自动协商
- TCP/TLS 连接复用
- 每主机连接上限
- DNS/TCP、TLS、响应头和流数据间隔分别超时
- 账号代理隔离
- 禁止上游重定向

账号隔离避免一个异常代理或连接池拖累其他账号，也便于后续增加 OAuth 刷新和私有协议 Adapter。

## 适配器运行模型

内置协议不增加进程。需要 OAuth、Cookie 或逆向协议的渠道才使用回环地址上的隔离进程。管理端读取适配器目录时才执行本机探针，单次超时 500 ms、结果缓存 60 秒、不跟随重定向，也不访问非 loopback 地址；普通模型请求完全不执行探针。状态被拆分为安装状态、进程状态、就绪状态和流量状态，避免把“已安装”误判为“已有凭据且可承载请求”。

额度可观测分两条轻量路径：Claude 从已选账号的真实上游响应中解析严格白名单字段；Codex、Gemini CLI 与 Antigravity 复用其官方 usage/quota 接口，但只由“渠道账号”页面按需唤醒，并按凭据设置 10 分钟内存 TTL。两条路径都在现有账号管理器锁内替换有界窗口切片；429 状态机额外提供模型冷却和重置时间。管理接口只序列化百分比、余额、模型、重置时间、观测时间和固定来源；不保存原始响应头、Token、Cookie 或请求正文。因此不需要数据库、Redis、常驻额度轮询器或额外进程，页面不可见时也没有额度网络请求。

## 安全

- 默认只监听 loopback。
- 模型 API Key 与管理员 Token 完全分离。
- 没有 Key 时 fail-closed。
- 托管 Key 明文只返回一次；磁盘仅保存 SHA-256 摘要，配置与 Key 文件均以 `0600` 原子写入。
- 每个 Key 可以限制模型、RPM、并发与过期时间，并可立即禁用或撤销。
- 管理入口先校验客户端 CIDR；只有明确配置的反向代理才可信任 `X-Real-IP`。
- 管理 Token 登录有按 IP 防爆破；成功后签发短期 `HttpOnly`、`Secure`、`SameSite=Strict` Cookie。
- Cookie 会话的非安全方法必须提交独立 CSRF Token；CLI 仍可从 loopback/VPN 使用管理员 Bearer Token。
- 上游 Key 优先来自环境变量。
- 不记录 Prompt、响应正文或密钥。
- 入站请求体、总并发、请求读取时间、响应头等待和流空闲时间均有上限。
- 敏感入站 Header 不透传；上游 `Set-Cookie` 不回传。
- HTTP 上游只允许 loopback，或明确开启的私网地址/单标签内部主机名。
- 上游重定向关闭，减少密钥被转发到意外主机的风险。

## 有意不实现

- 多节点一致性：单机不使用 Redis。扩展到多实例时需要用可替换状态层同步全局 RPM、并发租约与撤销事件。
- 用户、余额、计费、支付、订阅和团队权限。
- OAuth 快捷添加由固定版本 CLIProxyAPI 隔离处理；Lite 后端只代理平台白名单、回调与状态轮询，管理密钥不下发浏览器。Token 刷新由适配器负责。
- 任意协议之间的自动翻译。第一版只透明代理客户端所调用的协议。

这些边界保证服务维持单二进制和很小的攻击面。

适配器类型、契约、状态机和资源预算见 [Adapter Design](ADAPTER_DESIGN.md)。
