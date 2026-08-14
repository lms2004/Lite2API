# Architecture

## 目标

Lite2API 是单节点、单管理员的个人 AI 网关。设计优先级依次是：流式正确性、密钥安全、故障隔离、低延迟、低运维成本。

## 请求路径

```text
Client
  -> ingress API key + global in-flight guard
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
- 配置以 `0600` 原子写入，上游 Key 优先来自环境变量。
- 管理 API 不使用 Cookie，不存在基于 Cookie 的 CSRF 会话。
- 不记录 Prompt、响应正文或密钥。
- 入站请求体、总并发、请求读取时间、响应头等待和流空闲时间均有上限。
- 敏感入站 Header 不透传；上游 `Set-Cookie` 不回传。
- HTTP 上游只允许 loopback，或明确开启的私网地址/单标签内部主机名。
- 上游重定向关闭，减少密钥被转发到意外主机的风险。

## 有意不实现

- 多节点一致性：没有 Redis，多个实例之间不会共享并发槽。
- 用户、余额、计费、支付、订阅和团队权限。
- 自动 OAuth 登录和 Token 刷新。此类能力应按 Provider 单独实现。
- 任意协议之间的自动翻译。第一版只透明代理客户端所调用的协议。

这些边界保证服务维持单二进制和很小的攻击面。
