# Lite2API 权限与信息边界改造记录

主责 Agent：`/root/permissions`。仓库：`/root/Lite2API`。目标：用户提供的“Lite2API 业务能力图”，主要覆盖管理权限、控制用量、保护信息、受控变更。未访问生产接口或执行浏览器流程，未改动用户原有未跟踪文档。

## 已证实并修复

1. `ClientKeyStore.Authenticate` 与 `validCredential` 原将 `expiresUnix <= 0` 视为无期限，导致 1970-01-01 及以前的已过期 Key 被放行。现在以是否填写 `expires_at` 区分无限期，所有已填写且过期的时间都拒绝。
2. 管理配置快照和运行账号快照原暴露 BaseURL 查询凭据及 ProxyURL 用户信息。`information.go` 新增统一脱敏函数；管理展示隐藏所有 URL 查询值、代理用户名和密码。`runtime.go` 的 Snapshot 由 routing Agent 同步调用该函数。
3. 脱敏展示往返更新可能把掩码写成真实 API Key/URL。现在账号更新、连接测试、批量导入共用回填逻辑：完全未改的掩码 URL 恢复原值；修改过的掩码 URL 需提供完整新值；原有 API Key/自定义 Header 掩码恢复原值，新账号缺原始材料明确拒绝。显式授权导出仍保留可恢复原始材料。
4. `probeAccountModels` 原自动跟随重定向，可能向同主机新端口转发认证材料，并把跳转后成功误记为原连接成功。现在不跟随；3xx 作为未成功的原始状态返回。独立 transport 在退出时清理空闲连接，网络错误及 Endpoint 查询值脱敏，不返回上游自定义状态文本。
5. 正常调用原允许入站 `X-Admin-Token` 与 `X-CSRF-Token` 传向上游。已请 completion Agent 在其主责 `gateway.go` 的过滤集合中修复，本 Agent 补充真实请求断言，验证管理/CSRF/Cookie/客户端 Key 均不传出，而 Authorization 正确替换为上游凭据。
6. 配合 capability schema 扩展，`admin.go` 的 cloneAccount 深拷贝 `Features` map，避免外部调用者通过配置快照篡改内部事实。

## 证据与验证

定向命令：

```sh
GOTOOLCHAIN=local go test -race ./internal/gateway -run 'Test(AdminSnapshotsRedactURLCredentialsAndPreserveRoundTrip|AccountConnectionTestRestoresDisplayedSecrets|ProbeAccountModels|ManagedClientKey|ManagedClientPermissions)' -count=1 -timeout=60s
```

结果：通过，`ok github.com/lms2004/lite2api/internal/gateway 2.470s`。

覆盖的实际业务路径：

- `TestManagedClientPermissionsAffectCatalogCallsAndRevocation`：模型目录权限过滤、禁止模型不触及上游、允许模型真实完成调用、Key 模型权限更新和配置 Reload 后持续拒绝、删除 Key 立即拒绝目录读取、旧 legacy Key 从配置移除并 Reload 后被拒绝；还验证敏感入站 Header 不发给上游。
- `TestManagedClientKeyEditsPreserveUsageAndActiveLeases`：Key 元数据编辑复用原 RPM/并发 runtime，保留当前分钟计数，原请求租约能释放共享计数。
- `TestManagedClientKeyRejectsEpochAndPreEpochExpiry`：两条认证入口均拒绝 epoch、epoch 前、0001 年及通常过期时间。
- `TestAdminSnapshotsRedactURLCredentialsAndPreserveRoundTrip`：管理 HTTP 状态接口不含凭据 canary；配置更新保留真实材料；部分改动的掩码 URL 拒绝且配置不变；显式导出仍可恢复。
- `TestAccountConnectionTestRestoresDisplayedSecrets`：来自脱敏展示的连接测试实际携带原始 query/API Key/Header，但结果不泄露这些值。
- `TestProbeAccountModelsDoesNotFollowRedirects`：重定向目标 HTTP 服务收到 0 次请求，原连接保持 302/未成功。
- `TestProbeAccountModelsRedactsNetworkErrorURL`：连接失败消息没有 query 密钥。

后续增量添加 `TestAccountImportHandlesDisplayedSecretsWithoutSavingPlaceholders`，覆盖现有账号导入回填和新账号拒绝掩码。定向命令扩大到 `Test(AccountImport|AdminSnapshotsRedactURLCredentialsAndPreserveRoundTrip|AccountConnectionTestRestoresDisplayedSecrets|ProbeAccountModels|ManagedClientKey|ManagedClientPermissions)` 后再次 `-race -count=1 -timeout=60s` 验证通过，耗时 6.996s。

## 一次独立专项审查

主 Agent 要求对新增 `response_completion.go` 及交付集成进行一次独立审查。新增 `completion_review_test.go`，复现并确认：

- 扁平 path 字符串允许字面字段 `choices[]`/`data[]` 冒充结构节点；
- 重复 choices/output 字段导致旧证据保留而客户端最后值已覆盖；
- 缺少 message 的 Chat、缺真实 payload 的 embedding/image 被视为完成；
- 不同协议的 status/stop_reason 证据串用；
- SSE Responses 缺 output、Anthropic 孤立 message_stop、非流请求收到 SSE 被判成功。

最初 11 个场景全部在修复前失败，复现确证；另补 2 个空 data 元素场景。发现已交给唯一主责 completion Agent 修复。修复后本 Agent 独立执行 `GOTOOLCHAIN=local go test ./internal/gateway -run '^TestCompletionReview' -count=1 -timeout=30s`，13 个场景均通过，耗时 0.179s；`git diff --check` 同时通过。无需重复全仓审查。

## 保留的合理实现与边界

- 托管 Key 修改本来就共享使用计数，保留其实现，新增回归证据；Key 热路径仍不访问磁盘。
- `server.api_keys`/`api_key_env` 是显式保留的 legacy 全模型凭据，不具备单 Key RPM/并发/有效期；仍受全局、请求大小、队列和来源资源限制。需要细粒度约束时使用托管 Key。撤销需从所有配置与环境来源移除并 Reload；不能把它描述成与托管 Key 相同权限能力。
- 账号导出包含凭据是受管理认证与 CSRF 保护的显式操作，不应与普通展示一同脱敏，否则无法恢复。
- `/models` 连接测试仅证明该目录请求的可达及鉴权结果，不证明实际生成、工具、图片、流或质量。
- 当前证据为源码与隔离执行，未作为实际常用客户端/真实上游的生产验收。主 Agent 统一完成仓库级测试、文档和最终边界说明。
