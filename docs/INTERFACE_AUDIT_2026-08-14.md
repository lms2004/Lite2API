# Lite2API 迁移与接口质量审计报告

审计日期：2026-08-14（UTC）
审计对象：`/root/lite2api` 当前生产候选版本 `oauth-ui-20260814`
生产入口：模型 API `https://sub2api.foresights.top/lite/v1`；管理页 `https://sub2api.foresights.top/lite-admin/`（仅 VPN）

> 本文是 2026-08-14 的不可变审计快照，其中账号数量、容器状态和真实上游结果不代表当前生产状态。当前部署拓扑、服务状态检查、备份范围与固定版本以 `deploy/server-ops/README.md` 和 `deploy/server-ops/VERSION-MANIFEST.md` 为准。

## 1. 结论摘要

- 线上已部署快捷 OAuth 添加账号：选择平台后生成并复制授权链接，完成认证后粘贴回调 URL，页面自动提交、轮询、保存凭据并确认账号池热加载；Kimi 使用设备授权自动检测。
- 管理页不再要求输入管理员 Token。应用仅在显式启用 `LITE2API_ADMIN_AUTO_LOGIN=true` 且来源通过应用 CIDR 与 Nginx VPN 白名单时签发短期会话；所有写接口继续要求 CSRF。
- Lite2API、CLIProxyAPI、Grok2API、Gemini Web 和 Atom 服务健康端点均返回 200；Lite2API 与 CLIProxyAPI 容器均为 healthy。
- 当前汇总 95 个模型、6 个 Lite 逻辑账号、23 个适配器目录项。旧 Sub2API 的 8 个实际账号已按凭据边界迁移。
- 核心接口中 Chat（流式/非流式）、Responses、Embeddings、Images 均真实成功；Anthropic Messages 因旧 Claude refresh token 失效返回 401；Rerank 没有可用模型，返回 503。
- 未发现 P0（越权、凭据泄漏、配置半写入）问题。当前主要风险是已迁移凭据的外部有效性与无候选模型时的 30 秒等待。

## 2. 工具链与变更边界

未在宿主机安装 Go、Node 或其他长期工具链。构建和验证使用固定/官方 Docker 镜像：

- 构建：项目 `Dockerfile`（Go 1.23 Alpine，多阶段构建）
- 单元测试与 vet：`golang:1.25-alpine`
- 竞态测试：`golang:1.25-bookworm`（需要 CGO）
- 前端脚本检查与联调：`node:22-alpine`

这种方式不增加宿主机包管理状态，适合当前轻量单机部署。运行时 Lite2API 仍是单个静态 Go 二进制；Node/Go 容器只用于构建和审计。

## 3. 旧 Sub2API 账号迁移审计

旧系统共 8 个实际账号：

| 原账号类型 | 数量 | 迁移目标 | 当前状态 |
|---|---:|---|---|
| Gemini API Key | 1 | Lite2API 原生 Gemini OpenAI 兼容账号 | 已迁移；真实 Chat 200 |
| Gemini OAuth | 2 | CLIProxyAPI 隔离凭据目录 | 已加载为 active；实际推理 403 |
| Anthropic setup/refresh token | 1 | CLIProxyAPI 隔离凭据目录 | 已加载，但 refresh token 已失效，error/unavailable |
| Antigravity OAuth | 2 | CLIProxyAPI 隔离凭据目录 | active；真实推理 200 |
| Grok OAuth | 1 | Grok2API 数据卷 | 已迁移；当前客户端 Key 校验 401，Lite 逻辑账号保持停用 |
| OpenAI/Codex OAuth | 1 | CLIProxyAPI 隔离凭据目录 | active；Responses 真实调用 200 |

CLIProxyAPI 管理接口的脱敏汇总为：Antigravity active 2、Codex active 1、Gemini CLI active 2、Claude error/unavailable 1。凭据文件、邮箱、Token、管理密钥均未写入报告或测试输出。

## 4. 快捷 OAuth 添加账号审计

### 4.1 页面流程

1. 点击“添加账号”。
2. 直接选择 OpenAI/Codex、Claude、Gemini CLI、Antigravity 或 Kimi。
3. 页面调用 `POST /admin/api/oauth/start`，生成授权链接并尝试复制到剪贴板。
4. 用户复制或打开链接完成平台认证。
5. Codex/Claude/Gemini/Antigravity：复制浏览器最终 localhost 回调 URL，粘贴后页面自动调用 `POST /admin/api/oauth/callback`。
6. 页面持续调用 `POST /admin/api/oauth/status`；成功后凭据由 CLIProxyAPI 原子写入隔离目录，Lite 确认 `cliproxy-oauth` 账号池存在并热加载。
7. Kimi 为设备授权，步骤 5 不需要人工粘贴。

手动 API Key、自定义 Base URL、代理、模型映射和请求头仍可从“手动添加”次入口进入。

### 4.2 安全与真实联调

| 验证项 | 结果 |
|---|---|
| CLIProxy 专用管理密钥 | 仅服务端环境变量；浏览器不可见 |
| 管理目标 URL | 强制为 loopback IPv4/IPv6 的 HTTP URL，阻止外部 SSRF |
| Provider | 后端白名单：codex、anthropic、gemini、antigravity、kimi |
| 授权链接 | 必须为无 userinfo 的 HTTPS URL |
| 回调 URL | 只解析 http(s) URL 的 code/state，不向该 URL 发起请求 |
| CSRF | 三个 OAuth 接口均为 POST；无 CSRF 的真实请求返回 403 |
| CIDR | 伪造 `X-Real-IP: 198.51.100.8` 的真实登录请求返回 404 |
| 真实授权启动 | Codex 链接生成成功，返回合法 HTTPS URL/state，状态为 wait |
| 回调与自动建池 | 使用假适配器完成服务端端到端测试；真实完成仍需用户在平台页面交互 |

## 5. 模型 API 实测

所有请求均使用最小提示，响应正文不写入报告。Token 数值来自上游返回的 `usage`，未提供时标记为 N/A，不能当作 0。

| 任务 | 模型 | HTTP | 延迟 | 输入 Token | 输出 Token | 总 Token | 结果 |
|---|---|---:|---:|---:|---:|---:|---|
| Chat 非流式 | `gemini-2.5-flash-lite` | 200 | 527 ms | 6 | 1 | 7 | 通过 |
| Chat 流式 + `include_usage` | `gemini-2.5-flash-lite` | 200 | 635 ms | 6 | 1 | 7 | 2 个 SSE frame，包含 `[DONE]` |
| Responses | `gpt-5.4-mini` | 200 | 807 ms | 307 | 5 | 312 | 通过 |
| Anthropic Messages | `claude-haiku-4-5-20251001` | 401 | 30,254 ms | N/A | N/A | N/A | 旧 refresh token 无效 |
| Anthropic Messages（替代模型） | `claude-sonnet-4-5-20250929` | 401 | 30,231 ms | N/A | N/A | N/A | 同一 Claude 凭据不可用 |
| Embeddings | `gemini-embedding-001` | 200 | 349 ms | N/A | N/A | N/A | 返回向量；兼容响应无 usage |
| Images | `gpt-image-2` | 200 | 24,031 ms | 36 | 229 | 265 | 返回 1 张图，响应约 1.03 MB；未保存图片 |
| Rerank | `audit-rerank-unconfigured` | 503 | 30,071 ms | N/A | N/A | N/A | 无 Rerank 账号，`upstream_unavailable` |

流式请求未传 `stream_options.include_usage` 时仍正确结束，但上游不返回 usage；显式传入后得到 6/1/7。

### 5.1 输入与鉴权负向测试

| 测试 | 预期 | 实际 |
|---|---:|---:|
| `/v1/models` 无 Key | 401 | 401 |
| `/v1/models` 错误 Key | 401 | 401 |
| `/v1/models` 正确 Key | 200 | 200（95 个模型） |
| 非法 JSON | 400 | 400 |
| 缺少 `model` | 400 | 400 |
| 未支持路径 | 404 | 404 |
| Nginx HTTPS 无 Key | 401 | 401 |
| Nginx HTTPS 正确 Key | 200 | 200 |

## 6. 适配器真实可用性

| 适配器/任务 | 模型 | HTTP | 延迟 | Token（入/出/总） | 结论 |
|---|---|---:|---:|---|---|
| CLIProxy / Codex | `gpt-5.4-mini` Responses | 200 | 807 ms | 307/5/312 | 可用 |
| CLIProxy / Antigravity | `gemini-3-flash-agent` | 200 | 2,199 ms | 6/1/91 | 可用；总量含上游额外计算量 |
| CLIProxy / Gemini CLI | `gemini-2.5-flash` | 403 | 1,177 ms | N/A | 凭据已加载，但上游拒绝 |
| CLIProxy / Claude | 两个 Claude 模型 | 401 | 约 30.2 s | N/A | refresh token 已失效 |
| Atom | `deepseek-v4-flash` | 200 | 2,310 ms | 15426/22/15448 | 可用，但系统提示开销异常高 |
| Gemini Web | `gemini-3.6-flash` | 200 | 900 ms | 5/0/6 | 可用；usage 字段存在非加和差异 |
| Grok2API | `grok-4.5` | 401 | 73 ms | N/A | 当前 `GROK2API_KEY` 无效；逻辑账号停用合理 |
| Gemini 官方 API Key | `gemini-2.5-flash-lite` | 200 | 527 ms | 6/1/7 | 可用 |

所有已安装服务的健康 URL均返回 200。健康只代表进程可达，不等于账号推理成功；表中真实调用用于区分两者。

## 7. 管理 API 实测

审计创建了一个禁用占位账号和一个临时客户端 Key，并在 `finally` 中删除。结束后再次检查：Lite 账号数为 6，`audit-live-*` 账号和 Key 均为 0。

| 接口/任务 | HTTP | 验证内容 |
|---|---:|---|
| `POST /login`（空 body Token 字段） | 200 | VPN 自动会话签发 |
| `GET /state` | 200 | 运行状态与脱敏配置 |
| `GET /adapters` | 200 | 23 个目录项 |
| `GET /client-keys` | 200 | 列表读取 |
| `POST /client-keys` | 201 | 一次性返回 secret；随后删除 |
| `POST /accounts/import` dry-run | 200 | `applied=false`、预计新增 1 |
| `POST /accounts/import` apply | 200 | 原子新增并热加载 1；随后删除 |
| `POST /accounts/export` | 200 | 仅导出临时账号，数量 1，未输出正文 |
| `PUT /routes` | 200 | 原样写回当前路由 |
| `POST /reload` | 200 | 热加载成功 |
| `DELETE /accounts/:id` | 200 | 临时账号清理 |
| `DELETE /client-keys/:id` | 200 | 临时 Key 清理 |
| `POST /logout` | 200 | 会话销毁 |
| OAuth start/status | 200 | 真实 Codex 状态为 wait |
| OAuth callback | 单元集成通过 | 真实回调需要用户平台授权，不伪造完成 |

## 8. 自动化验证

- `go test ./...`：通过
- `go vet ./...`：通过
- `go test -race ./internal/gateway`：通过
- 管理页嵌入结构测试：通过
- 浏览器 JavaScript 语法检查：通过
- `git diff --check`：通过
- Nginx 实际页面：HTTP 200，存在“快捷添加账号 / 复制链接 / 提交并自动添加”，不存在管理员 Token 输入

新增 OAuth 服务端测试覆盖：Provider 白名单、管理密钥仅服务端转发、回调 payload、状态轮询、模型发现、自动创建账号池、非 loopback URL 拒绝。

## 9. 质量问题与建议优先级

### P1：需要重新认证或修复运行密钥

1. Claude 迁移 refresh token 已失效：使用新的快捷 Claude 授权重新登录。
2. Gemini CLI 两个凭据虽为 active，但真实请求 403：重新 OAuth，并核对 Google 项目/Code Assist 权限。
3. Grok2API 当前 Client Key 返回 `invalid_api_key`：在 Grok2API 内重新签发 Client Key，写入 `.env` 后再启用 `grok-local`。

### P2：接口质量改进

1. 无候选模型（当前 Rerank）等待完整 30 秒才返回 503；应在调度器确认候选集合为空时立即失败。
2. Atom 极短请求产生 15,426 输入 Token，应审计适配器注入的系统提示、上下文或 usage 口径。
3. Embeddings 上游兼容响应不含 usage；报告与监控应展示“未知”而非 0。
4. Claude 认证失败也等待约 30 秒，需检查 failover/queue timeout 是否在明确 401 后不必要地等待。

### P3：体验完善

1. 可在快捷授权完成后显示新增凭据的脱敏 provider/状态，而不仅显示账号池已就绪。
2. 可以为 Gemini 增加可选项目选择器；当前默认由适配器自动选择项目。
3. 真人 OAuth 全流程需由管理员实际点一次每个平台完成验收；自动化不能替用户授权，也不应伪造成功。

## 10. 资源与回滚

部署后资源快照：

- Lite2API：约 1.99 MiB / 256 MiB，7 PIDs，healthy
- CLIProxyAPI：约 11.33 MiB / 256 MiB，7 PIDs，healthy

部署前 Lite 镜像已保留为 `lite2api:rollback-before-oauth-ui-20260814`。CLIProxyAPI 继续固定在 v6.10.9 / commit `785b00c3127eea6aa207f1207ead8a2aa93690a3`，只监听 `127.0.0.1:45682`，管理面板关闭。
