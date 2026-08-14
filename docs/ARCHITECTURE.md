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
  -> healthy account filtering
  -> strategy ordering
  -> atomic account concurrency acquire
  -> account-isolated HTTP connection pool
  -> upstream
  -> streaming copy with idle watchdog
  -> release account concurrency
```

调度和运行配置封装在不可变的 `runtimeState` 中，通过原子指针整体切换。热加载不会让请求看到一半新、一半旧的配置。

客户端 Key 同样使用不可变 map 快照。管理端创建、更新或撤销时先以 `0600` 原子落盘，再切换指针；请求线程只做 ID map 查找、SHA-256 常量时间比较和原子计数。流式请求直到响应结束才释放 Key 与账号并发槽。最近请求使用固定环形缓冲区，写入为 O(1)。

## 调度

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
