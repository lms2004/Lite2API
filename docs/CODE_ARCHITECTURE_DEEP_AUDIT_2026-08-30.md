# Lite2API 全栈代码架构深度审计与优化报告

> **实施状态更新（2026-08-30）：** 本文是修复前基线。对应优化已经落盘，逐项结果、验证证据和未完成边界见 [CODE_ARCHITECTURE_OPTIMIZATION_2026-08-30.md](CODE_ARCHITECTURE_OPTIMIZATION_2026-08-30.md)。当前口径为 56 项已修复、8 项已缓解、4 项延期。

> 审计日期：2026-08-30
>
> 审计基线：`main@4c494be`（`feat: harden account routing and failover`）
>
> 审计范围：Lite2API 核心、配置/控制面、调度与代理、管理 API、管理前端、第三方适配器集成、Compose/systemd、迁移脚本、CI/CD、供应链与运维文档
>
> 审计方式：源码静态分析、跨模块数据流追踪、并发/生命周期推演、现有测试与 race/vet/coverage 验证
>
> 边界：本次没有修改业务代码、没有触达生产服务、没有读取 `.env` 内容、没有执行真实上游请求或浏览器端到端测试。`third_party/grok2api` 与 `third_party/gemini-web2api` 在当前工作树未初始化，因此只审计了 gitlink、构建/运行配置和 Lite2API 集成契约；未声称对这两个上游项目内部源码做了完整安全审计。

## 1. 执行摘要

Lite2API 已具备一个单机 AI 网关应有的若干良好基础：不可变状态指针切换、按账号隔离的 HTTP Transport、客户端 Key 摘要存储、常量时间比较、管理 CIDR/CSRF/HttpOnly Cookie、上游重定向关闭、请求日志有界队列与轮转、配置写盘采用临时文件和 rename。这些设计方向是正确的。

但当前实现的主要风险并不在这些独立组件内部，而在组件交界处。代码宣称的关键不变量——“先准入再读体”“热重载请求只看到一个代际”“空路由不可承载流量”“配置更新不会互相覆盖”“回环网络等价于隔离边界”“同一版本对应同一产物”——在真实组合路径上并不成立。

本次审计确认四项应立即止血的 P0 问题：

1. 删除路由最后一个账号后，空账号集合会被运行时解释成“所有已启用账号”，可能把敏感请求投递到未授权供应商。
2. Chat/Responses 请求在真实 Key RPM、Key 并发和全局并发门禁之前被完整读取；合法 Key 可用并发大请求绕过资源预算。
3. `model` 没有长度上限，固定 512 条的统计环可长期保留数百个超大字符串；默认配置下理论保留量可达约 32 GiB。
4. Grok 默认使用可变 `latest` 镜像、宿主网络、默认容器能力，且 Compose 未固定非 root UID，破坏整个“loopback 即可信”的密钥隔离模型。

问题索引共收录 68 项：P0 4 项、P1 30 项、P2 30 项、P3 4 项。这里的数量表示当前基线下已经找到证据链的问题，不表示对所有输入、第三方镜像和生产环境做过形式化完备证明。

此外，高风险问题集中在六条系统性链路：

- 控制面是“锁外复制旧快照、锁内整份覆盖”，并发管理、自动发现、SIGHUP 之间存在确定的 lost update。
- Scheduler 的容量通知会丢唤醒，热重载后旧 lease 又只通知旧 Scheduler。
- 熔断状态缓存并跨客户端回放完整上游错误响应，形成跨 Key 数据复用。
- 流式 watchdog 把上游空闲和下游慢消费混为一谈，Handler 可能在 copy goroutine 仍写响应时返回并提前释放并发槽。
- 前端由 v5 到 v12 的十套 CSS、十四层 JS 同时补丁式运行，已产生可利用的持久型 DOM XSS、会话和异步生命周期错误。
- Compose、systemd 和 CI 没有统一、可复现的适配器构建链；同一 CLIProxyAPI 版本标签可代表不同功能，现有回滚步骤也无法恢复真实命名卷。

### 1.1 总体判断

当前版本适合“受控、单管理员、低并发、熟悉现场状态”的个人节点实验，不宜在未处理 P0/P1 前扩大为多租户、多人共享 Key 或高并发生产网关。问题并不要求引入 Redis、数据库或微服务；首要工作是补齐单机内的事务、生命周期、资源预算和交付可复现性。

建议采用演进式修复，而不是一次性重写：先封住隔离与资源漏洞，再建立统一内部契约，最后拆包和替换前端补丁栈。

## 2. 风险分级与问题总表

### 2.1 分级定义

| 等级 | 定义 | 处置要求 |
|---|---|---|
| P0 / 严重 | 可直接突破租户/供应商隔离、造成持久大规模资源耗尽，或让不可信容器进入高价值信任域 | 停止扩容；72 小时内止血并补回归测试 |
| P1 / 高 | 可造成配置丢失、错误路由、跨请求数据复用、长期不可用、凭据暴露或不可恢复发布 | 进入最近一个修复迭代，作为发布阻断项 |
| P2 / 中 | 在特定条件下导致协议错误、状态误判、运维失真、局部资源泄漏或显著维护风险 | 1—2 个迭代内修复并建立契约测试 |
| P3 / 低 | 确定存在但影响局部，或主要是可维护性/可访问性问题 | 随相关重构清理，避免继续累积 |

### 2.2 核心与控制面问题索引

| ID | 级别 | 问题 |
|---|---:|---|
| CORE-01 | P0 | 删除最后一个路由账号后，空集合退化为全账号 wildcard |
| CORE-02 | P0 | 请求体在真实限流/并发门禁前读取，并在网关内二次读取 |
| CORE-03 | P0 | 超长 `model` 可使固定条数统计环持久占用巨量内存 |
| CORE-04 | P1 | 配置更新锁外取旧快照、锁内整份覆盖，确定存在 lost update |
| CORE-05 | P1 | Scheduler 容量通知丢唤醒，热重载跨代通知失效 |
| CORE-06 | P1 | 缓存并向后续客户端回放完整上游错误响应 |
| CORE-07 | P1 | 流 watchdog 可在 copy goroutine 仍写 ResponseWriter 时返回 |
| CORE-08 | P1 | 熔断粒度过粗、并发结果乱序、配置换身份仍继承旧健康状态 |
| CORE-09 | P1 | 环境覆盖层与 desired config 混合并被周期性写回磁盘 |
| CORE-10 | P1 | 请求日志无完整生命周期，reload 可产生同路径双 writer |
| CORE-11 | P1 | 两套路由 schema 的校验与运行时语义不一致 |
| CORE-12 | P1 | 热重载接受不生效字段，失败 reload 还可能产生部分副作用 |
| CORE-13 | P1 | 管理 Token 轮换不撤销旧 Cookie 会话 |
| CORE-14 | P1 | capability 合并丢失 effort 到 upstream model 的一一映射 |
| CORE-15 | P1 | `ok`、HTTP status、Error 三套结果口径互相矛盾 |
| CORE-16 | P2 | `runtimeState` 含可变 map/slice，并非真正不可变快照 |
| CORE-17 | P2 | BaseURL 固定 query 被入站 query 覆盖或在探测中清空 |
| CORE-18 | P2 | prompt-test 可无限占账号槽，释放也不唤醒正常 waiter |
| CORE-19 | P2 | 配置/Key 持久化缺少严格 schema、EOF、目录 fsync 与路径冲突校验 |
| CORE-20 | P2 | 错误未类型化、Anthropic 端点仍返回 OpenAI envelope |
| CORE-21 | P2 | `/health` 混合 liveness/readiness，并永久信任陈旧最后样本 |
| CORE-22 | P2 | 自动 failover 对生成类 POST 缺少幂等/不确定结果边界 |
| CORE-23 | P2 | 能力矩阵不能表达“某模型只支持某 operation/feature” |
| CORE-24 | P2 | 当前分钟保存全部 latency，P95 计算 O(n log n) 且内存无界 |
| CORE-25 | P2 | 代理未按 `Connection` token 动态剥离 hop-by-hop header |
| CORE-26 | P2 | 自动发现把观测数据写入 desired config，且服务/发现使用不同凭据快照 |
| CORE-27 | P2 | 适配器探针在全局锁内串行网络调用，readiness 结论过于宽松 |
| CORE-28 | P2 | legacy 环境 Key 没有 managed Key 的模型/RPM/并发策略 |
| CORE-29 | P3 | 旧式 round-robin 第一次请求从第二个账号开始 |

### 2.3 前端问题索引

| ID | 级别 | 问题 |
|---|---:|---|
| UI-01 | P1 | 账号 ID 可进入动态内联 `onclick`，形成持久型 DOM XSS |
| UI-02 | P1 | 管理会话失败/过期后不恢复，所有 fetch 无统一超时和取消 |
| UI-03 | P1 | 删除连接不展示路由影响，放大 CORE-01 的危险语义 |
| UI-04 | P1 | v5—v12 CSS/JS 全部叠加运行，行为依赖加载与 RAF 顺序 |
| UI-05 | P1 | 字节锚点构建失败时静默保留旧片段，可发布混合版本页面 |
| UI-06 | P1 | 内联事件/手写模板迫使 CSP 保留 `unsafe-inline`，失去第二道防线 |
| UI-07 | P2 | OAuth 轮询在 Esc 关闭后继续，旧请求可污染新授权会话 |
| UI-08 | P2 | API Key、Cookie、导入文件等敏感数据没有统一清理生命周期 |
| UI-09 | P2 | 批量导入可绕过 dry-run，预览与 apply 参数可不一致 |
| UI-10 | P2 | 前端把桶 P95 均值或最后一桶标成范围 P95 |
| UI-11 | P2 | 轮询整块替换 DOM，展开态、焦点和读屏上下文丢失 |
| UI-12 | P2 | 路由新增、本地选中态、导出选择集存在幽灵/覆盖状态 |
| UI-13 | P2 | OAuth routing、quota 初始化失败后被错误缓存或永久短路 |
| UI-14 | P2 | “测试全部渠道”没有成本预算、取消、单次或批次超时 |
| UI-15 | P2 | 趋势请求用共享 boolean 表示并发，快速切换会重叠请求 |
| UI-16 | P2 | 提示词实验室未遵守服务端消息/字节预算，且反复全量渲染 |
| UI-17 | P2 | 导入提示按源文件大小判断，与最终 1 MiB API body 契约不一致 |
| UI-18 | P2 | 单行全局状态同时持有服务器数据、草稿、timer 和秘密，无所有权 |
| UI-19 | P3 | 构建号被旧层覆盖，运行页面最终显示 v7 而非 canonical v14 |
| UI-20 | P3 | 嵌套 `<main>` 及不完整的 ARIA Tab/Radio 键盘语义 |

### 2.4 集成、部署与供应链问题索引

| ID | 级别 | 问题 |
|---|---:|---|
| DEP-01 | P0 | Grok `latest + host network + 默认能力/未固定 UID` 破坏 loopback 信任边界 |
| DEP-02 | P1 | Compose 与 systemd 用同一 CLIProxy 版本号构建出不同功能 |
| DEP-03 | P1 | systemd 安装器会把额外 dirty/untracked 源码伪装成固定版本 |
| DEP-04 | P1 | 根 CI 看不到 submodule、patch、Compose、Docker 和安装脚本回归 |
| DEP-05 | P1 | 两套 systemd 升级非事务，失败后无自动回滚 |
| DEP-06 | P1 | Compose 回滚手册与命名卷、镜像标签的真实模型不一致 |
| DEP-07 | P1 | 干净 clone/非 root 用户按渠道文档执行会因 submodule/UID 失败 |
| DEP-08 | P1 | Lite2API “安装器”不能在新主机创建运行前提 |
| DEP-09 | P1 | Grok 读取 `/app/config.yaml`，生成配置却挂载到 `/run/grok2api` |
| DEP-10 | P1 | 迁移脚本会覆盖同名账号、部分失败非事务、文件 owner 不匹配 |
| DEP-11 | P1 | Docker build context 包含 `.env` 与 `channels/runtime/` |
| DEP-12 | P1 | 写权限 CI 动态安装未锁 npm 包后 `git add -A` 并 push |
| DEP-13 | P1 | Nginx、配置模板、运维文档中的管理 allowlist 已漂移 |
| DEP-14 | P2 | CLIProxy Web UI 替代路径的临时 OAuth callback 绑定 `0.0.0.0` |
| DEP-15 | P2 | 长效管理/API Key 进入 `curl` 进程命令行 |
| DEP-16 | P2 | Compose 健康检查只证明 CLIProxy 根路径响应，不证明可承载流量 |
| DEP-17 | P2 | Base image、Actions、Go toolchain 和 build metadata 不可复现 |
| DEP-18 | P2 | 备份集中保存同机明文凭据，无异地、加密、retention、恢复演练 |
| DEP-19 | P3 | 残留 root AtomCode unit 与当前端口/容器架构不相连 |

## 3. 当前架构与有效设计

### 3.1 当前关键数据流

```text
Client
  -> routeExecutionProfileHandler
       -> 仅验证 Key 密码学有效性
       -> 读取并缓存 Chat/Responses body
  -> ServeGateway
       -> 再次 Authenticate / RPM / Key concurrency
       -> global in-flight
       -> 再次读取 body / JSON map
       -> Scheduler.Select
       -> per-account http.Client
       -> retry/failover/circuit/cache
       -> streaming copy/watchdog

Admin/UI ----------------------> Gateway admin handlers
                                   -> clone state.cfg（锁外）
Capability discovery ---------->   -> Store.Save（锁内整份覆盖）
SIGHUP ------------------------> Reload -> new Scheduler/new log writer

Compose/systemd -> local adapters -> loopback HTTP -> upstream providers
```

这个拓扑说明了根本矛盾：原子指针保证的是“一次 Load 得到完整对象”，但当前一个请求、一次更新和一个长生命周期 lease 都跨越多个 Load、多个 Scheduler 代际或多个持久化层。因此“对象内部不可变”没有自动转化为“业务事务一致”。

### 3.2 值得保留的设计

- `ClientKeyStore` 的写操作在 `writeMu` 内完成，先持久化再发布快照；密钥只保存 SHA-256 摘要，认证使用常量时间比较。
- 账号有独立 Transport，关闭上游重定向，支持连接复用、代理隔离和响应头超时。
- 已向客户端发送成功头后不继续拼接另一个上游流，避免跨提供方响应混合。
- 配置和 Key 文件使用 `0600` 临时文件、文件 `fsync` 与 rename；虽然仍缺父目录 `fsync`，基础方向正确。
- 管理面有 CIDR、可信代理、登录防爆破、HttpOnly/Secure/SameSite Cookie 和 CSRF。
- 请求明细不记录 prompt/response 正文，日志队列和轮转有上限。
- 主 Go module 的现有单元测试、race 和 vet 均通过，说明基础并发原语没有被现有用例直接击穿；问题主要落在测试尚未覆盖的代际、事务和故障语义。

## 4. 核心数据面与控制面深度分析

### 4.1 CORE-01：删除最后一个路由账号后变成全账号 wildcard（P0）

**证据链**

- `internal/gateway/admin.go:303-318` 删除账号时会从每条路由的 `Accounts` 和 `Targets` 移除引用，但保留空路由。
- `internal/gateway/runtime.go:314-320` 把空 `Accounts` 解释为所有已启用账号。
- `internal/gateway/runtime.go:450-469` 对“路由存在但 allowed 为空”的情况不限制账号，且因为 `routed == true` 不再执行普通的 `account.supports(model)` 检查。
- `internal/gateway/accountdelete_test.go:9-35` 只验证引用被移除，没有验证最后一个引用删除后的调度结果。

**触发场景**：模型别名只指向账号 A；系统还有 B/C 两个已启用账号；管理员删除 A。

**影响**：原本应 unavailable 的模型别名会转发到 B/C。请求可能被送到不同供应商、不同区域或不同合规边界，产生内容越界、错误计费和模型不兼容。这不是单纯的 503，而是 fail-open 路由。

**根因**：空集合同时承担“没有显式限制，使用全部账号”和“显式删除后没有目标”两种相反语义。

**整改**：

1. 立即止血：删除最后一个引用时删除或禁用对应路由，禁止保留空目标。
2. schema 层引入明确的 `all_accounts: true`；空集合永远表示无目标。
3. 配置加载时编译为 canonical route target 列表，运行时不再解释旧字段的隐式语义。

**验收**：删除最后 target 后 `/health` 和请求均显示该别名 unavailable；在任何策略下都不能选中其他账号。增加旧式 `Accounts`、新式 `Targets`、两者混用和删除最后元素的表驱动测试。

### 4.2 CORE-02：准入前读取请求体，资源预算可被合法 Key 绕过（P0）

**证据链**

- `internal/gateway/routeprofiles.go:28-55` 在 middleware 只调用 `validCredential`，随后读取最多 `MaxBodyBytes`。
- `internal/gateway/clientkeys_preflight.go:9-38` 明确说明该检查不消耗 RPM 和并发。
- `internal/gateway/gateway.go:197-236` 真正的 Key lease、RPM、Key concurrency、global guard 和第二次 body 读取均在 middleware 之后。
- 默认 `MaxBodyBytes` 是 64 MiB：`internal/config/config.go:20,139-140`；默认 global inflight 是 256。
- `docs/ARCHITECTURE.md:9-21` 声称顺序是 Key 限制、global guard、bounded body，实际相反。

**触发场景**：持有一个仍有效但已超过 RPM/并发的 Key，并发发送 chunked Chat/Responses 大请求。每个请求都会先在 middleware 完整读入，再被真正门禁拒绝。

**影响**：global inflight 对这部分内存完全不生效；同一 body 随后还会经历重新读取、`RawMessage`、改写和 inspection 等复制。默认理论值 `64 MiB × 256` 已超过 16 GiB，而绕过 global 后没有这个数量上限。Compose 虽覆盖为 8 MiB/32，但容器只有 256 MiB，仍缺少字节级预算。

middleware 和 `ServeGateway` 各自 `g.state.Load()`，若上传期间发生 reload，同一请求还可能用旧路由加 `service_tier`、再用新路由选择账号，直接违反“单请求只见一个配置代际”。

**整改**：

- 移除请求体预读 middleware，将 execution profile 改写放入 `ServeGateway` 单次解析之后。
- 固定顺序为：协议识别 → 完整 Key authenticate/RPM/lease → global request lease → global byte lease → 单次有界读/解析 → 路由/改写。
- 为 body 分配增加全局在途字节 semaphore；按 Content-Length 预留，chunked 请求边读边计费。
- 整个请求只加载一次 runtime snapshot，并通过 context/参数向下传递。

**验收**：1000 个并发 chunked 大请求中，被 RPM/Key concurrency/global 拒绝的请求在读取 body 前返回；进程 RSS 有确定上界；reload 中的请求始终使用同一 config revision。

### 4.3 CORE-03：超长 model 可造成持久内存膨胀（P0）

**证据链**

- `internal/gateway/gateway.go:245-249` 只要求 model 非空，没有长度/字符集约束。
- `internal/gateway/gateway.go:274-287` 把完整 model 写入 `RequestRecord`。
- `internal/gateway/stats.go:123-135` recent ring 只限制 512 条，不限制总字节。
- `internal/gateway/requestlog.go:122-127` 超长日志记录会被从字节尾部截断，产生无效 JSON。

**触发场景**：使用没有模型白名单的合法 Key，顺序提交 512 个 model 接近 body 上限、但最终找不到账号的请求。

**影响**：请求完成后，统计环仍长期引用这些大字符串。默认上限下约为 `512 × 64 MiB ≈ 32 GiB`；Compose 的 8 MiB 覆盖下仍约 4 GiB。请求日志同时被写成无法再解析的截断 JSON，破坏重启后的观察基线。

**整改**：入口对 model、route alias、account ID、provider request ID 等所有可观测维度设置独立长度和字符集；统计结构增加总字节预算；日志编码前截断字段而不是截断序列化后的 JSON。

**验收**：超限 model 在分配大统计对象前返回 400；512 条最坏记录的内存占用受固定预算约束；每一条轮转日志都能独立 JSON decode。

### 4.4 CORE-04：配置 read-modify-write 不是事务（P1）

**证据链**

- `UpsertAccount`、`DeleteAccount`、`ReplaceRoutes` 在 `internal/gateway/admin.go:260-326` 锁外 clone 当前 state。
- `reloadMu` 直到 `saveAndReload` 的 `internal/gateway/admin.go:329-340` 才获取。
- Import 在 `internal/gateway/accountimport.go:116` 读取旧快照，`:168` 才保存。
- 能力发现于 `internal/gateway/capabilitysync.go:49-76` 捕获旧 state，串行完成多个最长 12 秒的网络请求后，在 `:110-114` 整份覆盖。

**触发场景**：两个管理员并发保存；管理员保存与自动 discovery 重叠；SIGHUP 外部编辑与 UI 更新重叠。

**影响**：最后拿到锁的请求不代表基于最新 revision，而是用更早 clone 覆盖整个文件。自动发现尤其可能在数十秒后“复活”刚删除账号、删除刚新增路由或覆盖人工调整。

**根因**：`reloadMu` 只串行化 commit，不串行化 read-modify-write；配置没有 revision/CAS；人工 desired state 与自动观测 state 共用同一个 JSON。

**整改**：

```go
type ConfigRepository interface {
    Snapshot() (RawConfig, Revision)
    Update(ctx context.Context, expected Revision, mutate func(*RawConfig) error) (Revision, error)
}
```

- 所有人工变更在同一事务中读取最新 revision、mutate、验证、落盘、发布。
- 管理 API 返回 `ETag`/revision，更新要求 `If-Match`；冲突返回 409 而不是静默覆盖。
- discovery 网络请求在锁外执行，但提交时按 account ID 和采样 revision 做三方合并；远端 catalog 存入独立 DiscoveryState，不整份写 desired config。

**验收**：并发 Upsert/Replace/Discovery barrier 测试中最终结果必须包含所有不冲突变更；相同字段冲突明确返回 409；revision 单调增加。

### 4.5 CORE-05：Scheduler 丢唤醒与跨代 lease（P1）

**证据链**

- `internal/gateway/runtime.go:189-195,224-225` 每个 Scheduler 使用容量为 1 的共享 `notify` channel。
- waiter 在 `:342-377` 竞争消费一个通知。
- release 在 `:432-440,494-499` 非阻塞发送；channel 满时通知直接丢失。
- reload 只共享 `accountRuntimeState`，旧 Selection 的 closure 仍引用旧 Scheduler 的 notify。

**触发场景**：多个槽近同时释放且多个 waiter 正在等待；或满载长流期间 reload，新请求在新 Scheduler 等待，旧流结束后通知旧 Scheduler。

**影响**：实际已有容量，部分或全部请求仍睡到完整 QueueTimeout；不同账号/模型的 waiter 可能消费无关通知，公平性和延迟都不可预测。

**整改**：容量状态和通知器必须属于跨 reload 持久的 AccountRuntime/RuntimeRegistry；使用 generation broadcast channel、`sync.Cond` 或公平 semaphore，不用 cap=1 共享事件队列表达“状态已变化”。`Selection.Release` 同时应具备幂等保护。

**验收**：N 个 waiter/N 个槽同时释放，所有 waiter 在小于一个调度 tick 内获得容量；reload 前旧 lease 释放能立即唤醒 reload 后 waiter；`-race` 和重复 10,000 次压力测试均稳定。

### 4.6 CORE-06：跨客户端回放上游错误体（P1）

**证据链**

- `internal/gateway/runtime.go:146-160` 把完整 `bufferedResponse` 保存在账号状态。
- `internal/gateway/gateway.go:380-393,678-687` 缓存最多 1 MiB body 和允许通过的 headers。
- 后续请求在 `internal/gateway/gateway.go:321-329` 直接写回这个响应。
- `internal/gateway/gateway_test.go:246-278` 当前测试反而锁定了“第二请求收到相同 body/header”的危险行为。

**触发场景**：客户端 A 触发 401/402/403/429/5xx 并打开 circuit；客户端 B 用另一个 Key 请求相同 model/operation。

**影响**：B 会收到 A 的 provider request ID、陈旧 Retry-After、诊断信息；若上游错误体回显 prompt、文件名、参数或账号信息，会形成跨客户端数据泄露。429 最长 circuit 可达 24 小时，且相同账号 ID reload 后还可能继续继承缓存。

**整改**：账号状态只保存标准化 failure class、status、retry deadline 和安全错误码；为每个新请求重新生成协议正确的错误。原始 body/header 最多只在当前请求自己的 failover 链中使用，绝不能进入跨请求 runtime state。

**验收**：A 的错误 body/header 中植入 canary，B 的响应中不得出现；reload、24 小时 Retry-After 和多个 Key 场景均覆盖。

### 4.7 CORE-07：stream watchdog 与 Handler 生命周期脱节（P1）

`internal/gateway/gateway.go:618-640` 在 goroutine 中执行 `io.Copy`；timer 超时后关闭 upstream body 并立即返回，没有等待 goroutine。`activityWriter` 又只在对下游 `ResponseWriter.Write` 返回后报告 activity（`:650-658`）。因此慢客户端会被误判成“上游 idle”，而 Handler 返回后 goroutine 仍可能继续使用已经失效的 ResponseWriter；账号、Key、global lease 也会提前释放。

应把 upstream read idle 与 downstream write deadline 分开：使用可取消读、`http.ResponseController.SetWriteDeadline` 或同步 pump；任何路径都必须等 copy 完整退出后再释放 lease 和返回 Handler。验收要使用阻塞 ResponseWriter、无法立即被 Close 打断的 Body、慢客户端和真实取消四类夹具。

### 4.8 CORE-08：熔断不是并发安全的状态机（P1）

`internal/gateway/runtime.go:126-143` 中任意成功都会清空 failure/circuit/cache；任意 401/429 可强制打开整个账号 circuit。若一次更早发起的慢成功晚于 401/429 完成，它会错误关闭刚打开的 circuit。状态只按账号保存，单模型 429 会封禁该账号所有模型/operation；`NewSchedulerWithPrevious` 又只按账号 ID 复用整个 state（`:224-235`），更换 BaseURL、凭据或 adapter 后仍可能继承旧 circuit 和旧响应缓存。

应拆分：

- `CapacityState`：只保存 active lease，跨 identity 更新可安全复用。
- `HealthState`：按 `(account identity fingerprint, operation, upstream model, error class)` 建 breaker。
- breaker 使用 closed/open/half-open 状态和 generation；只有当前 generation 的探针成功可以关闭。
- credential、BaseURL、adapter、proxy 等身份字段变化时重置健康/cache，保留必要的旧 lease 计数直到自然归零。

### 4.9 CORE-09/12/16：配置分层、热重载和不可变性名不副实（P1/P2）

- `internal/config/config.go:193-267` 把环境覆盖直接写进 Config；`Store.Save` 在 `:626-663` 又序列化该 effective Config。任何账号/路由保存或自动 discovery 都会把临时环境值永久写入 JSON，尤其影响 AdminAutoLogin、管理 CIDR 和资源上限。
- `internal/gateway/server.go:19,38` 的 Listen、RequestReadTimeout 只在 Run 启动时读取；Reload 却接受并发布它们，管理面显示新值而实际 listener 仍旧。
- Reload 在 `internal/gateway/gateway.go:97-107` 先更新 admin session TTL，再创建新 log writer；后者失败时 reload 返回错误，但 TTL 已改变。
- `Gateway.Config()` 直接返回含共享 map/slice 的 cfg；Normalize 仅浅拷贝 Accounts 后原地修改 Capabilities/Routes，Scheduler 也直接持有 routes map。

目标模型应拆成：

```text
RawConfig（版本化、仅 desired state、可持久化）
  + EnvironmentOverlay（只读、带来源）
  + DiscoveryState（有 TTL、provenance、不可自动覆盖人工配置）
  -> Compile/Validate
  -> EffectiveRuntimeConfig（深冻结、含 revision/fingerprint）
```

字段必须声明 `dynamic`、`restart-required` 或 `immutable-after-start`。所有资源先 stage 成功，再一次原子 commit；失败不得留下 TTL、writer、listener 等部分副作用。

### 4.10 CORE-10：请求日志没有 lifecycle owner（P1）

- `requestLogWriter.Close` 会 drain、Sync、Close：`internal/gateway/requestlog.go:184-194`。
- `Gateway.Run` 的所有退出路径只执行 HTTP shutdown：`internal/gateway/server.go:50-76`，从不关闭 request log。
- Reload 先打开同一路径新 writer，swap 后才 drain 旧 writer：`internal/gateway/gateway.go:100-112`。
- 两个 writer 同时持有相同路径时，旧 writer 可能 rotate/rename，新 writer 继续写到已成为 backup 的 file descriptor，currentBytes 和保留序列都会失真。

应由 `Gateway.Close()` 统一关闭 HTTP intake、等待请求、停止 discovery、drain log、flush/close transports。日志应保持一个长生命周期 actor，通过 control message 原子 reconfigure；或严格 freeze/drain 旧 writer 后再打开新 writer。增加 reload 跨轮转阈值和 SIGTERM 队列非空测试。

### 4.11 CORE-11/14/23：路由与能力 schema 无法表达真实兼容性（P1/P2）

当前同时存在：

- 旧式 `Accounts + UpstreamModel`；
- 新式 `Targets + Model + ReasoningEffort`；
- 账号级 `Operations`；
- `ChannelCapability{Model, UpstreamModel, []ReasoningEffort}`。

问题包括：

1. 旧式 route 只校验账号存在，不校验 upstream model；运行时对 routed account 跳过 `supports`，可稳定选中不兼容账号（`config.go:430-447`、`runtime.go:464-470`）。
2. 两套字段可同时出现，Targets 会静默覆盖 Accounts 的实际语义。
3. `capability_discovery.go:266-287` 按 logical model 合并多个 capability，只保留一个 UpstreamModel，却合并全部 effort；high/low 后缀模型可能最终都路由到同一个上游 ID。
4. capability 没有 operation/features 维度；同一模型在 Responses 可用、Chat 不可用，或仅支持 stream/tools/images 时无法表达。

建议统一为：

```text
CompiledTarget {
  account_identity
  operation
  logical_model
  profile/effort
  upstream_model
  features { stream, tools, vision, audio, files, json_schema }
  policy
}
```

旧配置只在 migration/compile 边界读取，运行时永远消费 canonical targets；混合字段必须拒绝而不是猜测。

### 4.12 CORE-13：管理 Token 轮换不撤销会话（P1）

Reload 会把新 token 写入 runtime，但只调用 `adminAuth.SetTTL`。Cookie 验证在 `internal/gateway/adminauth.go:141-161` 只查 session map，不绑定 token/config generation。攻击者取得 Cookie 后，即使管理员轮换 token 并 reload，该 Cookie 仍有效到原 expiry。

会话应记录 auth epoch/token fingerprint；管理员 token、auto-login 或安全网络策略变化时递增 epoch并清空旧 session。验收：轮换后旧 Cookie 立即 401，旧 CSRF 不可复用，新 Bearer 和新会话正常。

### 4.13 CORE-15/20/21/24：可观测性没有统一事实模型（P1/P2）

- 流断开时 `ok=false`，但 Record 仍保留 200；累计计数按 `ok` 算失败，trend/health 按 status 算成功。
- 排队期间客户端取消可能被统一编码成 503，污染上游健康。
- `/health` 的实现明确永久使用最后一条历史请求，不考虑年龄；数天前成功可以继续显示 ready。
- 当前分钟保存每次 latency，再复制排序计算 P95；高 RPS 下内存 O(n)、计算 O(n log n)。
- 前端又对分钟桶 P95 做平均或取最后一桶，进一步把错误数据展示成范围 P95。

应建立唯一 `Outcome`：

```text
success | client_cancelled | gateway_rejected | queue_timeout |
upstream_status | connect_error | header_timeout | stream_error |
downstream_timeout | uncertain_submission
```

HTTP 响应、breaker、累计指标、trend、health 和请求日志均从 Outcome 映射。使用 HDR Histogram/t-digest/固定桶直方图，不保留全部 latency。拆分 `/livez`、`/readyz`、`/health/details`；readiness 必须包含 freshness 和关键路由策略。

### 4.14 其他核心确定问题（P2/P3）

| ID | 证据与影响 | 整改与验收 |
|---|---|---|
| CORE-17 | `gateway.go:566-578` 用入站 RawQuery 覆盖 BaseURL query；discovery/test 又清空 query。依赖 `api-version` 的 Azure/兼容端点会失败 | 明确禁止 BaseURL query，或按固定参数优先、冲突拒绝的规则合并；三条调用路径做契约测试 |
| CORE-18 | `prompttest.go:96-101,143-150` 直接 acquire/release，不通知 Scheduler；响应头后无限 body 会永久占槽 | 统一走 Lease API；诊断有总超时、read-idle 和 8 MiB 上限；释放唤醒正常 waiter |
| CORE-19 | Config Load 不拒绝未知字段；Admin JSON 不检查第二个 JSON 值；文件 rename 后不 fsync 父目录；未禁止 config/key/log 路径相撞 | schema version + strict decoder + EOF；目录 fsync；比较 canonical/real path；错误配置、掉电和路径碰撞测试 |
| CORE-20 | `/v1/messages` 的网关错误仍为 OpenAI envelope；405 无 `Allow`；磁盘错误常被映射成 400/404 | 领域错误类型 + 中央 mapper；按 operation 输出 OpenAI/Anthropic 协议；状态码和 headers 契约测试 |
| CORE-22 | transport error、408/409/425/5xx 会自动重发生成 POST；上游可能已接收并计费 | 按 operation/失败阶段定义 retry policy；支持 provider idempotency key；记录 uncertain outcome；不可证明未提交时不自动重试 |
| CORE-25 | 静态 hop header 黑名单没有删除 `Connection` header 点名的动态字段 | 解析 Connection tokens 后逐项剥离；增加请求/响应双向代理协议测试 |
| CORE-26 | discovery 把远端 catalog 长期追加进配置；普通适配器不 prune；服务使用 runtime 缓存 Key，discovery 每次重新解析 env Key | 独立带 TTL/provenance 的 discovery cache；显式 promotion 才落 desired config；统一 versioned secret provider |
| CORE-27 | `adapters.go:147-157` 持全局 mutex 做网络；目录逐个串行探针；任意 2xx—4xx 即认为 running，非 CLI 只要有关联账号就 ready | 每 adapter singleflight + 有界并行；typed readiness contract 验证 auth/model/operation；失败域隔离 |
| CORE-28 | `clientkeys.go:201-205` legacy env Key 直接返回无限制 lease，没有模型/RPM/并发策略 | 明确标记仅 break-glass；支持为 legacy key 配策略或完成迁移后禁用；管理面显示风险 |
| CORE-29 | `runtime.go:669-674` legacy round-robin 使用 `counter % n`，counter 从 1 开始，首请求命中第二账号 | 与显式 target 共用 `(counter-1)%n` 实现；首轮确定性测试 |

## 5. 管理前端深度分析

### 5.1 UI-01：持久型、点击触发的 DOM XSS（P1）

`internal/web/native-v10.js:357-364` 把 `account.id` 插入动态内联事件：

```html
onclick="v10TestChannel('${encodeURIComponent(account.id)}')"
```

`encodeURIComponent` 不编码单引号和圆括号。账号 ID `x')-alert(1)-('` 会突破 JavaScript 字符串并形成可执行表达式。账号导入可把该值带入配置（`accountimport.go:261-268`），而后端验证只检查 ID 非空和重复（`config.go:371-379`）；HTML 表单的 `pattern` 不能保护导入/手写配置路径。

触发需要管理员导入或加载恶意配置，并在运行总览点击该账号“测试”，因此不是无认证远程 XSS；一旦触发，脚本运行在管理同源，可读取 sessionStorage CSRF、调用所有管理 API、发起凭据导出，并读取仍留在 DOM/全局变量中的新 Key 或 Cookie。

整改必须同时覆盖两个边界：

1. 禁止动态内联 handler，使用 `createElement`/可信模板与 `addEventListener` 闭包。
2. 后端统一收紧 account ID/route alias 字符集和长度；不能只依赖表单 pattern。
3. 完成事件迁移后移除 CSP `script-src 'unsafe-inline'`，引入 nonce/hash 或外部 ES module。
4. 加入上述 payload 的 DOM 单测和浏览器 E2E；静态规则禁止把动态值写入 `on*=`。

### 5.2 UI-04/05/06：前端不是版本替换，而是补丁叠层（P1）

`internal/web/embed.go:123-126` 同时拼接十套 CSS 和十四层 JavaScript。各层覆盖 `window.render`、`saveRoutes`、`showView`、`drawRequestChart`，再通过 wrapper、MutationObserver 和双 RAF 追补。最终行为依赖源文件顺序、DOMContentLoaded 注册顺序和帧调度。

`buildNativeIndexHTML` 又依赖原始 HTML 字节 marker 做 `replaceRange`；`embed.go:146-161` 在 marker 缺失时静默返回原页面，`embed_test.go:282-291` 甚至把这种静默行为写成期望。一个 anchor 漂移即可发布“部分旧页面 + 部分新脚本”的混合产物。

当前源片段约有 145 个内联 `onclick`，大量 `innerHTML` 和上下文无关的手工 `esc()`。服务端 CSP 只能保留 `script-src 'unsafe-inline'` 和 `style-src 'unsafe-inline'`，UI-01 因而没有 CSP 第二道防线。

**建议迁移路线**：

- 立即冻结新增 `native-vN` 文件。
- 建立单一 ES module 入口和单一构建号；按 `api-client`、`store`、`routes`、`accounts`、`monitor`、`dialogs` 拆分。
- 先迁移高风险动态 HTML/事件，再迁移全局 state 和 renderer；旧层通过 feature flag 逐页移除。
- 构建时任何 marker 必须恰好命中一次，否则失败；校验唯一 ID、唯一 main、最终脚本语法和 DOM 拓扑。

### 5.3 UI-02/07/13/15/18：异步状态没有 generation、取消和所有者（P1/P2）

- `index.html:219-224,475` 只在启动调用一次 `connectAdmin`；401 不清 CSRF、不单飞重登、不重放请求。所有 fetch 没有 AbortSignal/timeout，任一挂起可让 `loadBusy` 永久阻塞。顶栏仍静态显示“已连接”。
- OAuth polling 在显式 close 时停止，但 Esc 原生 close 只恢复焦点；旧在途响应会读取新的全局 `oauthSession`，污染新授权。
- OAuth routing 第一次读取失败即把 `oauthRoutingAttempted=true` 永久保留；quota 又可能在登录前请求并把 401 失败缓存 60 秒。
- trend 只用共享 boolean 表示并发，`force=true` 可叠加请求，旧请求 finally 又错误改写新请求 busy 状态。
- `index.html:216` 一行全局变量同时持有服务器 state、路由草稿、选中集、timer、File、新 Key 和 OAuth session，没有清晰所有权。

应建立明确状态机：`anonymous -> authenticating -> ready -> refreshing -> expired/offline`；所有请求带 request ID、generation、AbortController 和 deadline。服务器状态、UI 草稿、敏感临时状态、请求状态分仓；只有当前 generation 可以提交结果。dialog close/pagehide/view change 必须取消所属任务。

### 5.4 UI-03/08—17：操作正确性与运维口径问题（P1/P2）

| ID | 触发与影响 | 整改/验收 |
|---|---|---|
| UI-03 | 删除连接仅通用确认，后端却同步改写所有 route 引用；可触发 CORE-01 | 服务端提供 impact preview；前端列出受影响别名/剩余目标；空路由阻止或二次高危确认 |
| UI-08 | `lastCreatedSecret`、API Key input、Cookie/SSO textarea、File 引用关闭后不清理 | 统一 dialog teardown；成功/关闭/超时/切页清空；新 Key 短 TTL 和显式“已保存并清除” |
| UI-09 | “确认导入”默认可点，不要求成功 dry-run；mode 改变不使预览失效 | dry-run 成功后保存规范 payload+mode hash；只有完全一致才允许 apply；后端可选 preview token |
| UI-10 | 更大桶 P95 用分钟 P95 算术平均；无明细时用最后一桶冒充范围 P95 | 后端从可合并直方图计算范围 P95；否则准确命名为“桶 P95 均值/最新桶” |
| UI-11 | 15 秒刷新用 `innerHTML` 重建账号/OAuth DOM，details、焦点和读屏上下文丢失 | keyed patch；保存 open/focus/scroll；业务按钮由主 renderer 一次生成，不靠 Observer 追补 |
| UI-12 | 新路由被 localStorage 的旧 selected key 重新隐藏；删除账号后 selectedAccounts 保留幽灵 ID并使导出失败 | 单一组件 selected state；持久化只做副本；每次 state 更新与现存 ID 求交 |
| UI-14 | N 个渠道固定各做 3 次真实计费请求，无范围、取消、单次/批次超时 | 显示 3N 调用和预计成本；选择范围；AbortController；全批次预算和页面离开取消 |
| UI-16 | 提示词实验室每轮全量发送/重绘历史；前端不展示 64 条/256 KiB 服务端预算；响应可达 8 MiB | 实时预算、临界阻止/新会话；响应展示截断/下载；增量/虚拟列表 |
| UI-17 | 只校验源文件总计不超过 1 MiB，最终 JSON wrapper 会超服务端 1 MiB body | 按最终 UTF-8 `Blob.size` 校验；为 import 端点定义一致的独立契约 |
| UI-19 | canonical v14 构建号被 v5/v6/v7 双 RAF 最终覆盖为 v7 | 单一不可变 build 常量和一个渲染点；E2E 断言服务端版本等于页面版本 |
| UI-20 | dialog 内嵌套 `<main>`；Tab/Radio 只有 click，无方向键/Home/End/roving tabindex | 按 ARIA Authoring Practices 完整实现；自动 a11y 检查和键盘 E2E |

### 5.5 前端测试盲区

`internal/web/embed_test.go` 主要验证字符串存在/不存在，并明确要求多个历史层同时存在。Go package 的 89.8% coverage 主要覆盖 Go 拼装代码，不能代表 JavaScript 行为覆盖。所有 standalone JS `node --check` 通过只说明语法合法。

建议新增四层测试：

1. 纯函数：上下文编码、route normalize、范围 percentile、导入 body size。
2. DOM 单元：事件绑定、secret scrub、keyed patch、焦点/展开态。
3. API 契约：401 单飞重登、timeout、preview/apply 指纹、OAuth generation。
4. 少量浏览器 E2E：XSS payload、路由新增、删除影响、Esc 关闭 OAuth、键盘 Tab、快速切趋势。

## 6. 适配器、部署、CI 与供应链深度分析

### 6.1 DEP-01：Grok 容器破坏 loopback 信任边界（P0）

`compose.channels.yml:38-54` 默认拉取 `ghcr.io/chenyme/grok2api:latest`，使用 `network_mode: host`，没有固定 `user`、`cap_drop`、`read_only`、CPU/内存/PID 限制。Lite2API 和 CLIProxy 在 loopback 明文 HTTP 上发送长效管理/API Bearer（例如 `oauthadapter.go:591-620`）。

启用 Grok profile 后，恶意/被攻陷的 mutable image 位于宿主网络命名空间。Compose 未固定运行 UID；若镜像没有自行声明非 root `USER`，进程即以 root 运行，同时仍保留 `NET_RAW` 等默认能力，形成连接、监听或嗅探回环管理流量的条件。即使镜像当前碰巧声明非 root，mutable `latest` 也不能把该声明当成稳定安全不变量。此时 `127.0.0.1` 不再是适配器隔离边界，攻击可横向到 OAuth 凭据池和其他本机服务。

**立即措施**：在完成隔离前默认禁用该 profile；pin digest/签名或从固定 submodule revision 构建；改独立 internal bridge 网络或 Unix socket/mTLS；非 root、drop ALL、read-only、no-new-privileges、资源限额。

**验收**：`docker inspect` 无 host network、无 capabilities、非 root；Grok 容器无法连接/抓取 CLIProxy 管理通道；镜像 digest、签名和 SBOM 由 CI 验证。

### 6.2 DEP-02/03：同版本不同产物，版本元数据不可信（P1）

- Compose 在 `compose.channels.yml:4-11` 直接从 submodule 构建并标记 `v6.10.9-lite2api.5`。
- `third_party/cliproxyapi/Dockerfile:9-15` 只 COPY/build。
- 只有 `deploy/install-cliproxyapi-systemd.sh:14-18,57-65` 应用三组维护 patch。
- installer 只验证 HEAD，再直接编译当前工作树；它不拒绝额外 modified/untracked 文件，也不验证工作树恰好等于三组 patch。
- Lite2API 本体安装器同样在 `deploy/install-lite2api-systemd.sh:52-62` 只把 HEAD 写入版本字符串，随后从当前共享工作树编译，没有 clean tree 或精确 tree digest 校验。

因此干净 clone 的 Compose 缺少维护功能，而当前现场 dirty submodule 的 Compose 可能碰巧包含；systemd 则会应用 patch。三者都能声称相同版本/commit。任何额外现场 Go 文件也可被 installer 编进“固定版本”二进制。

应维护正式 fork commit，或从 pinned git object 创建 detached 临时 worktree，确定性应用有序 patch manifest，并以最终 tree digest 进入版本。容器和 systemd 对同一 manifest 运行同一契约测试。

### 6.3 DEP-05/06/08/09：安装、升级、回滚不是完整事务（P1）

- 两个 systemd installer 都先覆盖 binary/config/unit，再重启和健康检查；失败只退出，不恢复旧版。
- CLIProxy installer 会从模板重建整个 config，仅保留 routing strategy，其他合法运行参数被覆盖。
- Lite installer 要求 `/etc/lite2api/config.json` 已存在，却不创建 `lite2api` 用户、目录、env；unit 又强依赖这些前提。它是“已配置主机上的覆盖升级”，不是新机安装。
- Compose 运维文档只记录旧 image ID，没有创建 rollback tag；实际配置在命名卷 `lite2api-data:/app/data`，文档却让恢复宿主 `data/config.json`。
- Grok command 读取 `/app/config.yaml`，bootstrap 生成文件挂载到 `/run/grok2api/config.yaml`，随机 admin/JWT/credential key 可能根本不生效。

目标部署模型应使用版本化 release 目录、预启动 config check、原子 symlink 切换、失败自动回滚、schema-aware config migration。新机 provisioning 必须幂等创建用户/目录/权限/secret reference。Compose 升级前显式 tag 旧镜像并导出命名卷快照。

### 6.4 DEP-07/10：冷启动和迁移在普通用户场景不可用（P1）

- `channels/README.md:16-23` 未要求初始化 submodule；Gemini Dockerfile 必须 COPY 未初始化的 submodule。
- bootstrap 设置目录 0700/文件 0600，只有 root 执行才 chown 10001；Compose 却固定 CLIProxy/Gemini 为 UID 10001。普通 docker-group 用户按文档执行会启动失败。
- 迁移脚本的文件名主要来自 email/label；相同邮箱账号会普通截断覆盖。发现 unsupported 项之前可能已写入一部分文件，失败非事务；文件又属于执行脚本的宿主 UID，容器可能不可读。

bootstrap 必须做完整 preflight：初始化/校验 submodule、选择明确 owner、以 init container/named volume 设置权限。迁移先生成 collision manifest 和完整 dry-run，在 staging dir 全部成功后原子发布；默认 `O_EXCL` 拒绝覆盖，文件名包含稳定 source ID。

### 6.5 DEP-11/12/13/15：密钥和发布信任边界不完整（P1/P2）

- 根 `.dockerignore` 只排除 `.git/data/binary/log`，没有排除真实 `.env` 和 `channels/runtime/`；根镜像和 Gemini 都使用仓库根 context。即使当前 Dockerfile 未 COPY，远端 builder、共享 daemon 和 cache 仍会收到这些密钥材料。
- `apply-canonical-refactor.yml` 在 `contents: write` job 中动态安装未锁定 `playwright-core`，随后 `git add -A` 并 push；Actions 也使用浮动 major tag。
- Nginx include、`config.example.json` 和 server-ops 文档中的管理 CIDR 集合不一致，模板重新部署可能锁死管理员或继续允许旧出口。
- `deploy/nginx-subpath.conf:42-46`、`config.example.json:7` 与 `deploy/server-ops/README.md:155-159` 的实际集合不同；这不是抽象风险，而是当前仓库已经存在的配置漂移。
- installer/check 脚本把长效 Bearer 放在 `curl -H` 命令行，本机其他进程可在未启用 hidepid/ProtectProc 时短暂读取。

整改：每镜像最小 context；显式 ignore secrets/runtime/backups；只用 BuildKit secret。写 token 与不可信 npm/browser job分离，依赖 pin SHA/lockfile，提交只允许白名单路径。用单一结构化来源生成两层 allowlist并在部署前集合比对。探针 key 经受限 FD/stdin config、Unix socket 或短期 token 传递。

### 6.6 其他部署与供应链问题（P2/P3）

| ID | 证据与影响 | 整改/验收 |
|---|---|---|
| DEP-04 | `ui-quality.yml` paths 忽略 deploy/compose/Docker/submodule/patch；checkout 不递归；根 `go test` 不进入独立 CLIProxy module | clean-clone CI 初始化 submodule、应用 manifest、main+submodule test/race、Compose build/smoke、ShellCheck、迁移 fixture、SBOM/扫描 |
| DEP-07 | Grok/Gemini submodule URL 使用 `git@github.com`；没有 GitHub SSH 凭据的公共 clone/CI 无法初始化，且渠道文档没有说明 | 改 HTTPS 或显式声明私有依赖/凭据；用无 SSH key 的 clean runner 验证唯一启动文档 |
| DEP-12 | 三个 self-modifying workflow 目标旧 feature branch，部分引用当前不存在的 `internal/web/app.js`/fixture，且有 `git add -A` | 删除一次性 workflow，或改只读生成 artifact + 人工 review PR；禁止 job 自写同一触发分支 |
| DEP-14 | CLIProxy `auth_files.go:132-147` 在 `is_webui=true` 替代路径绑定 `0.0.0.0`；Lite 当前主粘贴回调路径未传该参数，因此不是每次 OAuth 都触发 | callback 只绑定 127.0.0.1/::1；所有 OAuth 流程运行时用 `ss` 断言没有全接口 listener |
| DEP-16 | CLIProxy healthcheck 只 GET 根路径；无凭据、模型为空、补丁接口损坏仍可能 healthy | 拆 liveness/readiness；readiness 验证 config、管理 auth、模型 auth 和至少一个可用模型 |
| DEP-17 | Go/Alpine base 多数只 pin tag；Actions 只 pin major；CLI installer 用 `GOTOOLCHAIN=auto`；build date 为手工旧常量 | pin digest/SHA/toolchain；生成 SBOM、漏洞报告、SLSA provenance、签名；重复构建 digest 验证 |
| DEP-18 | 备份把 OAuth/env/VLESS 等明文集中放同机 `/root/server-backups`，仅同目录 SHA，无加密/异地/retention/恢复演练 | 客户端加密后异地/不可变存储，独立 KMS，定期隔离 VM restore drill |
| DEP-19 | `atomcode-daemon.service` 以 root 在 13456 运行；配置/文档使用独立容器 45678，仓库无桥接 | 删除陈旧 unit，或明确 daemon→adapter 架构、专用用户和健康检查 |

## 7. 跨模块根因

### 7.1 “快照”被误当作“事务”

原子指针只能保证读者看到完整对象，不能保证跨两个 Load 的请求一致，也不能保证基于旧快照的写者不覆盖新值。需要显式 revision、事务更新和请求级 snapshot pinning。

### 7.2 一个结构同时承载 desired、effective、observed、runtime

Config 同时保存人工期望、环境覆盖和远端 discovery；AccountRuntimeState 同时保存容量、统计、熔断、缓存；前端单一 global state 同时保存服务器状态、草稿和秘密。不同生命周期混在一起，reload、失败和取消自然难以正确。

### 7.3 空值、旧字段和兼容路径承担隐式语义

空账号集合可能是全账号，也可能是无账号；零并发是无限；旧路由和新路由混用时由运行时猜测；legacy Key 与 managed Key 权限不同却共享入口。兼容性没有在 migration 边界收敛，复杂度泄漏到每个请求。

### 7.4 lifecycle 资源没有统一 owner

Scheduler notifier、request log writer、capability discovery、HTTP server、admin session、前端 polling 和 OAuth task 都缺少明确的 start/reconfigure/stop owner。reload 和页面切换因此产生跨代资源。

### 7.5 交付产物没有唯一来源

submodule gitlink、dirty tree、三组 patch、Compose Dockerfile、systemd installer 和 mutable image 各自决定一部分最终行为。版本字符串没有绑定最终 tree、工具链、base image 和配置 schema。

## 8. 建议目标架构

### 8.1 单节点内的目标分层

```text
                     Control Plane
Admin/API/UI -> ConfigService -> ConfigRepository(revision/CAS)
                     |                    |
                     |              RawConfig vN
Discovery Worker -> DiscoveryStore(TTL/provenance)
                     |
             Config Compiler/Validator
                     |
        EffectiveSnapshot(revision, deep-frozen)
                     |
                     v
                      Data Plane
Ingress -> ProtocolCodec -> KeyLease -> Global/ByteLease -> One Body Parse
       -> CompiledRoute -> RuntimeRegistry -> AttemptPolicy -> Transport
       -> StreamPump -> Outcome -> Metrics/Log
                              |
                CapacityState | HealthBreakerState
                （不同生命周期、不同 key）
```

### 8.2 建议包边界

| 包 | 职责 | 不应拥有 |
|---|---|---|
| `controlplane/configrepo` | revision、CAS、schema migration、durable write | HTTP proxy、scheduler |
| `controlplane/discovery` | catalog 采样、TTL、provenance、三方合并 | 直接整份保存 desired config |
| `routing/compiler` | 把所有 legacy schema 编译为 canonical targets | 网络和运行统计 |
| `runtime/registry` | 跨 reload 的账号 identity、容量 lease、通知 | HTTP envelope |
| `runtime/breaker` | 按 operation/model/error class 的状态机 | 原始 response body |
| `proxy/attempt` | 幂等边界、failover policy、uncertain outcome | config persistence |
| `proxy/protocol` | OpenAI/Anthropic 路径、错误 envelope、header policy | UI/日志 |
| `proxy/stream` | read idle、write deadline、取消、完整退出 | detached ResponseWriter goroutine |
| `observability` | Outcome、bounded record、histogram、health projection | prompt/response 正文 |
| `server/lifecycle` | start/reload/shutdown、资源 staged commit | 领域配置修改 |
| `web` | 单一模块入口、store、API client、keyed renderer | 服务端秘密和长期全局 timer |

不建议为了拆包而立即增加进程。上述边界可以全部保留在一个二进制内，通过接口和不可变 DTO 隔离。

### 8.3 推荐请求顺序

```text
1. 识别 endpoint/protocol，拒绝未知方法和超大 headers
2. 读取一次 EffectiveSnapshot，并固定 revision
3. 完整认证并获取 Key RPM/concurrency lease
4. 获取 global request lease 与全局 byte lease
5. 单次有界读取/解析，校验 model 与字段长度
6. 从 compiled route 选择 account capacity lease
7. 按 operation/失败阶段执行安全 attempt policy
8. 同步、可取消的 stream pump；区分 upstream idle/downstream timeout
9. 生成唯一 Outcome，更新 breaker/metrics/log
10. 严格一次释放所有 lease
```

## 9. 分阶段优化路线

### Phase 0：72 小时内止血

1. 修复空 route wildcard：删除最后 target 后 route unavailable/删除。
2. 把 execution profile body 改写合入 `ServeGateway`；真实门禁前不读 body。
3. 为 model/account ID/route alias/观测字段加长度上限和日志字段级截断。
4. 删除跨请求 `bufferedResponse` cache，只保留标准化失败元数据。
5. 修复 UI 动态 inline handler XSS；后端收紧 ID 字符集。
6. 在隔离完成前默认禁用 Grok profile；至少修正 config mount 路径并 pin digest。

**退出门槛**：对应回归测试全部存在；大请求/RSS、跨 Key canary、恶意账号 ID、删除最后 target 四类测试通过。

### Phase 1：第 1—2 周，建立事务与生命周期

1. 实现 ConfigRepository revision/CAS；所有管理更新统一入口。
2. 分离 RawConfig、EnvironmentOverlay、DiscoveryState。
3. Scheduler notifier 移到持久 RuntimeRegistry，使用 broadcast/generation。
4. 分离 CapacityState/HealthState，加入 breaker generation/half-open。
5. 重做 stream pump 和 Gateway.Close；日志单 actor reconfigure。
6. 管理 auth epoch；token/安全策略变化立即撤销 session。
7. 前端统一 API client、session state、timeout、abort、secret teardown。

### Phase 2：第 3—6 周，收敛契约

1. 配置 schema version 与 canonical target migration；拒绝混合旧字段。
2. capability 增加 operation/profile/features 维度。
3. Outcome 和领域错误统一；OpenAI/Anthropic 分协议编码。
4. `/livez`、`/readyz`、`/health/details` 分离；延迟改可合并 histogram。
5. 前端迁移到单一 ES module/store/keyed renderer，逐页删除 v5—v12 补丁。
6. 对 retry 建立 operation/失败阶段矩阵和 uncertain outcome。

### Phase 3：第 4—8 周，交付与恢复闭环

1. CLIProxy 使用正式 fork 或确定性 patch manifest，容器/systemd 同源构建。
2. 所有镜像/Actions/toolchain pin digest/SHA；生成 SBOM、签名、provenance。
3. Compose 适配器移出 host network，使用最小权限和最小 build context。
4. systemd 采用版本 release + 原子切换 + 自动回滚；补齐新机 provisioning。
5. clean clone、普通用户、干净 VM、命名卷 restore、故障注入成为发布门禁。
6. 删除一次性自写 workflow 和陈旧 AtomCode unit，文档由有效配置生成。

## 10. 验证结果与测试盲区

### 10.1 已执行验证

| 命令/检查 | 结果 |
|---|---|
| 根 module `go vet ./...` | 通过 |
| 根 module `go test ./...` | 通过；沙箱首次因 httptest 监听权限失败，允许临时本机端口后通过 |
| 根 module `go test -race ./...` | 通过 |
| 根 module `go test -cover ./...` | config 71.3%、gateway 70.4%、web 89.8%、cmd 0.0% |
| CLIProxy submodule `go test ./...` | 通过 |
| CLIProxy auth/management/usage helper 目标包 `-race` | 通过 |
| `node --check internal/web/*.js` | 所有 standalone JS 语法检查通过 |
| 三组 CLIProxy patch reverse-check | 当前现场均显示已应用 |

当前 `third_party/cliproxyapi` 在审计前已是 dirty 工作树，包含维护 patch 的 modified/untracked 文件；本次没有修改它。这个事实同时说明“从共享 dirty tree 构建”的 provenance 风险是真实存在的，但不代表这些改动本身是错误。

`third_party/grok2api` 和 `third_party/gemini-web2api` 当前只有 gitlink、未初始化源码；再加上 Grok Compose 默认引用 mutable remote image，实际运行镜像的内部行为无法由本仓库基线唯一确定。这部分应作为残余供应链未知风险，而不是被本报告的 68 项已确认问题所掩盖。

### 10.2 绿色测试没有覆盖的语义

- 两个写者基于同一 revision 并发提交。
- 旧 Scheduler lease 释放唤醒新 Scheduler waiter。
- 多槽同时释放的公平唤醒。
- 删除最后 route target 后的真实调度。
- 准入前是否读取 chunked body、全局在途字节预算。
- 超长维度字段对长期 RSS 和日志 JSON 的影响。
- slow/blocking ResponseWriter 与 Body.Close 不可立即中断。
- cached 429/5xx 的跨 Key body/header 隔离。
- request log reload 恰逢 rotate 和 graceful shutdown drain。
- 管理 token 轮换后的旧 Cookie。
- effort suffix 到 upstream model 的一一映射。
- BaseURL 固定 query、Anthropic error envelope、queued cancel outcome。
- DOM XSS、OAuth Esc、401 重登、secret scrub、焦点保持、导入 preview/apply。
- clean clone 的 patch、Compose、UID、new VM、rollback 和 restore。

### 10.3 建议新增的发布门禁

1. **并发模型测试**：barrier、generation、lost wakeup、10000 次重复调度。
2. **资源测试**：大 body、超长字段、慢读/慢写、RSS/heap profile 上限。
3. **协议测试**：OpenAI/Anthropic envelope、header tokens、BaseURL query、取消与 uncertain outcome。
4. **安全测试**：XSS payload、CSP、跨 Key canary、token rotation、build-context secret canary。
5. **交付测试**：clean clone + submodule + patch manifest + image/systemd 行为一致。
6. **恢复测试**：坏二进制、坏配置、健康失败、命名卷丢失、干净 VM restore。

## 11. 可量化验收标准

| 领域 | 验收标准 |
|---|---|
| 路由隔离 | 空 target 永远不选择账号；每次选择可追溯到 compiled route revision |
| 请求资源 | 被认证/RPM/并发拒绝的请求不读取 body；body 在途总字节有硬上限 |
| 统计内存 | recent records 有总字节预算；任意字段不能单独突破预算；P95 不保存全部样本 |
| 配置事务 | revision 单调；不冲突更新合并；冲突明确 409；失败无部分副作用 |
| 调度 | N 个槽释放唤醒 N 个 waiter；reload 不改变活跃 lease 可见性 |
| 数据隔离 | 后续 Key 永远收不到前一请求原始 upstream body/header |
| 流生命周期 | Handler 返回前 copy 完整退出；所有 lease 恰好释放一次 |
| 管理安全 | token/security policy 变化立即撤销旧 session；CSP 无 `unsafe-inline` |
| 前端状态 | 所有异步 action 有 deadline、abort、generation；关闭 dialog 清除秘密 |
| 适配器隔离 | 不使用 host network；非 root、drop ALL、read-only；管理通道不可嗅探 |
| 构建来源 | 版本唯一绑定 source tree、patch set、toolchain、base digest、SBOM |
| 恢复 | 任一发布健康失败自动回滚；命名卷和凭据可在干净 VM 恢复 |

## 12. 最终结论

Lite2API 当前最大的问题不是缺少功能，而是“声明的架构边界与组合后的实际语义不一致”。尤其需要把以下四条原则变成代码级不可变条件：

1. 空集合绝不隐式扩大权限或路由范围。
2. 任何按请求分配的大内存都必须发生在完整准入和字节预算之后。
3. 人工配置、环境覆盖、远端发现和运行状态必须有不同的数据类型与生命周期。
4. 同一版本必须对应唯一可重建产物，loopback 只有在网络命名空间和权限隔离成立时才是安全边界。

先完成 Phase 0/1，可以在不改变“单节点、单管理员、单二进制”产品定位的前提下显著降低风险。完成 canonical route/config/outcome 契约后，再拆 `internal/gateway` 和前端补丁栈，重构成本会更低，也更容易证明行为没有改变。
