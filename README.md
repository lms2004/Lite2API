# Lite2API

面向个人使用的轻量 AI API 网关。它重新实现了大型中转平台里最实用的单机核心：连接复用、账号池、账号级并发限制、模型路由、粘性会话、熔断和失败换号。

项目是独立实现，不依赖 Sub2API，也没有 PostgreSQL、Redis、Node.js、计费、支付、多租户和用户系统。

## 已实现

- OpenAI 兼容 API 的流式、非流式透明转发
- `/v1/chat/completions`、`/v1/responses`、`/v1/messages`、embeddings、images 和 rerank
- `/v1/models` 汇总模型别名
- 多账号与账号级连接池
- 按请求操作类型筛选适配器能力，避免跨协议误路由
- `least_loaded`、`round_robin`、`priority`、`sticky` 四种调度策略
- 原子并发槽、最长等待时间、客户端取消传递
- 401/403/429/5xx 与网络错误自动换号
- 连续失败熔断和自动恢复
- 模型别名与上游模型重写
- 管理页面、快捷 OAuth 添加账号、一键生成客户端 API Key、账号增删改、批量导入/导出、适配器目录、实时状态和最近 200 条请求
- Sub2API/OpenAI 风格 `Authorization: Bearer` 与 `X-Api-Key` 接口
- 托管客户端 Key：模型白名单、RPM、并发、过期、禁用、撤销和使用统计
- Key 热路径使用 O(1) 内存快照、SHA-256 摘要和原子限流，不访问磁盘或数据库
- 管理 Token 与客户端 Key 分离；VPN 白名单可自动签发短期 HttpOnly 会话，所有写操作仍要求 CSRF
- 上游密钥支持环境变量，配置文件强制以 `0600` 原子写入
- 限制请求体、过滤敏感与 hop-by-hop Header、禁止上游重定向
- 入口总并发保护、慢请求读取保护和流空闲超时
- 单 Go 二进制，无第三方 Go 依赖
- AtomCode、Grok2API、Gemini Web、CLIProxyAPI 采用故障隔离的外置渠道适配器

> `type: anthropic` 表示使用 `x-api-key` 认证并透传 Anthropic 格式；第一版不在 OpenAI Chat Completions 与 Anthropic Messages 之间自动翻译。

## 快速启动

```bash
cd /root/lite2api
chmod +x deploy/bootstrap.sh
./deploy/bootstrap.sh
# 编辑 .env，填入实际的 ATOMCODE2API_KEY / DEEPSEEK_API_KEY
docker compose up -d --build
```

配置保存在 Docker 命名卷 `lite2api-data` 中；镜像首次启动时会写入安全的示例配置，管理页面后续可以原子更新它。默认只监听 `127.0.0.1:45679`：

- 管理页：<http://127.0.0.1:45679/admin>
- 健康检查：<http://127.0.0.1:45679/health>
- API Base URL：`http://127.0.0.1:45679/v1`

Compose 使用 Linux host network，以便直接访问同机 `127.0.0.1:45678` 上的 AtomCode2Api。对外服务时建议继续由 Nginx 终止 TLS，不要把管理端口直接暴露公网。

当前服务器的正式入口：

- API Base URL：`https://sub2api.foresights.top/lite/v1`
- 管理页：`https://sub2api.foresights.top/lite-admin/`（仅连接服务器 V2Ray/VPN 后可访问）
- 旧 Sub2API 已停止；域名根路径跳转到 Lite2API 的受鉴权模型列表。

## 一键创建客户端 API Key

VPN 内进入“API 密钥”页面，点击“一键生成 API Key”即可创建默认允许全部模型、不限 RPM、不限并发、永不过期的个人密钥，并自动尝试复制。明文只显示一次；名称、模型白名单、限速、并发和过期时间放在“高级创建”中。管理 API 也允许省略名称，服务端会自动命名。

## 调用

```bash
curl http://127.0.0.1:45679/v1/chat/completions \
  -H "Authorization: Bearer $LITE2API_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-fast","messages":[{"role":"user","content":"ping"}],"stream":true}'
```

模型别名通过 `routes.<alias>` 编排：路由只选择一次逻辑 `model` 与 `reasoning_effort`，`targets[]` 仅保存真实接入渠道及其 fallback 顺序。每个渠道在 `capabilities[]` 中声明自己支持的逻辑模型、推理强度与渠道专用上游 ID；管理页会自动筛除不兼容渠道，运行时再解析为对应上游模型。这里的渠道是 Antigravity、Claude Code 官方、Web 代理或 API 账号等实际凭据来源，不是 Quality、Balanced、Fast 一类虚拟档位。客户端始终使用稳定别名，不需要随渠道调整而改变配置。旧的目标级 `model` / `reasoning_effort` 以及 `accounts`、`upstream_model`、`strategy` 仍可读取。

## 热加载

管理页面保存账号后会原子更新配置并立即加载。手工修改配置后可以：

```bash
docker kill --signal HUP lite2api
```

或者调用：

```bash
curl -X POST http://127.0.0.1:45679/admin/api/reload \
  -H "Authorization: Bearer $LITE2API_ADMIN_TOKEN"
```

## 快捷添加 OAuth 账号

在 VPN 内打开“渠道账号”并点击“接入新账号”，可直接选择 OpenAI/Codex、Claude、Gemini CLI、Antigravity 或 Kimi。页面会生成并复制授权链接；完成认证后，将浏览器地址栏中的 localhost 回调 URL 粘贴回来，页面会自动提交、轮询结果、保存适配器凭据，并确认 `cliproxy-oauth` 路由池已热加载。Kimi 使用设备授权，页面自动检测完成状态，无需粘贴回调。

账号页按渠道归组认证凭据；路由连接收进次级折叠区。每次 OAuth 登录新增或更新的是隔离凭据，不应重复增加 `cliproxy-oauth` 路由行。Claude 从真实请求响应采集 5 小时、7 天和模型周窗口；Codex、Gemini CLI 与 Antigravity 在账号页可见时异步读取各自官方额度接口，每个凭据 10 分钟内最多一次。反重力的 Credits、模型桶以及所有渠道的 429 冷却/重置时间采用同一展示协议。CLIProxyAPI 只把脱敏的百分比、余额、模型、重置和观测时间保存在内存；没有可信数据时页面显示“等待观测”，不会伪装成 0%，也不打开全局响应头透传。

凭据只写入 CLIProxyAPI 的隔离目录。浏览器不会收到 `CLIPROXYAPI_MANAGEMENT_KEY`、模型 API Key、原始响应头、Token 或 Cookie；Lite2API 只通过回环地址代理受 CSRF 保护的 `POST /admin/api/oauth/start`、`POST /admin/api/oauth/callback`、`POST /admin/api/oauth/status` 和只读 `GET /admin/api/oauth/accounts`。账号页只在可见时每 15 秒读取内存快照，官方额度网络同步由 CLIProxyAPI 按凭据以 10 分钟 TTL 合并，其他页面不会持续读取认证池或探测上游。API Key、第三方兼容地址和高级请求头仍可从弹窗底部进入“手动添加”。

Gemini Web 与 Grok Web/Console 属于 Cookie/SSO 型接入。管理页只在本地浏览器整理扩展导出的 Cookie-Editor JSON、Netscape Cookie、单行 Cookie 或 SSO 文本，然后提示写入对应隔离适配器；Lite2API 核心不会接收或保存这些内容。为节省资源，未配置凭据的 Web 适配器保持停止，配置完成后才按需启动。

Docker Compose 与 systemd 两种部署方式均受支持。systemd 服务器可执行 `deploy/install-cliproxyapi-systemd.sh` 安装或升级固定版本适配器；脚本会校验固定子模块提交、幂等应用仓库维护的额度快照补丁、同步两把服务端密钥、启动服务，并验证管理与模型鉴权路径。完整步骤和凭据位置见 [渠道适配层](channels/README.md)。

## 批量导入与导出账号

管理页账号页右上角的“更多操作”包含“数据导入”和“数据导出”；勾选账号后会变为“导出选中”。导入支持同时选择或拖入多个 JSON 文件，浏览器逐文件校验并合并，随后可先执行预检查，再确认一次性保存并热加载。单次最多导入 500 个账号，请求体上限为 1 MiB。

支持 `lite2api-data`、`sub2api-data` 和旧版 `sub2api-bundle`。Lite2API 原生格式示例：

```json
{
  "type": "lite2api-data",
  "version": 1,
  "accounts": [
    {
      "id": "deepseek-main",
      "name": "DeepSeek API",
      "type": "openai",
      "adapter_id": "generic-openai",
      "instance_id": "deepseek",
      "operations": ["openai.chat"],
      "base_url": "https://api.deepseek.com/v1",
      "api_key_env": "DEEPSEEK_API_KEY",
      "models": ["deepseek-chat", "deepseek-reasoner"],
      "concurrency": 4,
      "enabled": true
    }
  ],
  "proxies": []
}
```

接口为 `POST /admin/api/accounts/import`，请求使用 `data`、`mode` 和 `dry_run` 字段。`mode` 可选 `skip`（跳过已有 ID）或 `upsert`（更新已有 ID；未提供密钥时保留旧密钥）。导入允许部分成功，并在 `errors` 中返回每一项失败的索引和原因。

导出接口为 `POST /admin/api/accounts/export`，可指定账号 ID，并可选择是否包含代理定义。导出文件包含可恢复的账号凭据，管理页会明确警告，文件应按密钥材料保管。两个写接口都要求登录会话和 CSRF；管理页面不会通过不安全的 GET 暴露凭据。

Sub2API 的 API Key 账号和代理会映射到 Lite2API。OAuth、Cookie、refresh token 等凭据不会被误当作 API Key；Codex、Claude、Gemini CLI、Antigravity 和 Kimi 可在“添加账号”中重新快捷认证，其他类型仍需先通过外部适配器暴露兼容 `base_url`。工作流参考来源见 [Third-Party Notices](THIRD_PARTY_NOTICES.md)。

## 安全边界

- 网关没有配置 API Key 时默认拒绝所有模型请求。
- 管理 Token 没有配置时默认关闭管理 API。
- 管理页和管理 API 同时受 Nginx 来源白名单与应用层可信代理/CIDR 校验保护；伪造 `X-Real-IP` 无效。
- VPN-only 部署可显式设置 `LITE2API_ADMIN_AUTO_LOGIN=true`，应用在 CIDR 校验通过后签发短期、`HttpOnly`、`Secure`、`SameSite=Strict` 会话；浏览器无需输入 Token，写操作仍需 CSRF Token。此选项默认关闭。
- 托管客户端 Key 的明文只显示一次；磁盘仅保存 SHA-256 摘要，撤销通过原子内存快照立即生效。
- 建议所有上游密钥只通过 `.env` 提供。
- HTTP 上游默认仅允许 loopback；AtomCode2Api 等同机服务需要显式开启 `allow_private_http_upstream`。
- 生产入口必须使用 HTTPS，并限制 `/admin` 的来源 IP 或仅通过 VPN 访问。

## 与完整 Sub2API 的取舍

Lite2API 只面向单机、单管理员。单机默认使用内存 Key/限流状态，避免 Redis 网络往返并减少故障点。未来只有在多实例部署时才需要接入 Redis 来同步全局 RPM、并发租约和撤销事件。

它不提供用户余额、套餐计费、支付、团队权限和复杂协议转换。OAuth 仅作为本机适配器的快捷账号授权流程存在，不承担终端用户登录；其他能力如果以后确实需要，应作为独立 Provider 或模块增加，而不是重新引入整套 SaaS 架构。

详细设计与运维说明见 [Architecture](docs/ARCHITECTURE.md) 和 [Operations](docs/OPERATIONS.md)；本轮真实接口、Token 和迁移质量结果见 [Interface Audit](docs/INTERFACE_AUDIT_2026-08-14.md)。

## 适配器目录与可选渠道

管理页“适配器”页面和 `GET /admin/api/adapters` 提供统一目录，区分原生就绪、运行就绪、待授权、运行未配置、未运行和仅收录状态，并展示操作类型、认证方式、本机延迟与模型数量。运行探针只在读取管理目录时按需执行，结果缓存 60 秒；普通请求路径没有轮询、注册中心或额外网络开销。“收录”不等同于自动安装或信任第三方代码。

Grok2API、Gemini Web 与 CLIProxyAPI 已作为固定提交的 Git 子模块或固定源码接入，并使用独立进程隔离，不增加 Lite2API 核心镜像体积。开发或整套容器部署可使用独立 Compose profile；当前单机生产也提供 CLIProxyAPI systemd 安装器。旧 Sub2API 凭据拆分迁移、启动和升级流程见 [channels/README.md](channels/README.md)；新增适配器的类型选择、能力契约和资源预算见 [Adapter Design](docs/ADAPTER_DESIGN.md)。
