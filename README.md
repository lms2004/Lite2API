# Lite2API

面向个人使用的轻量 AI API 网关。它重新实现了大型中转平台里最实用的单机核心：连接复用、账号池、账号级并发限制、模型路由、粘性会话、熔断和失败换号。

项目是独立实现，不依赖 Sub2API，也没有 PostgreSQL、Redis、Node.js、计费、支付、多租户和用户系统。

## 已实现

- OpenAI 兼容 API 的流式、非流式透明转发
- `/v1/chat/completions`、`/v1/responses`、`/v1/messages`、embeddings、images 和 rerank
- `/v1/models` 汇总模型别名
- 多账号与账号级连接池
- `least_loaded`、`round_robin`、`priority`、`sticky` 四种调度策略
- 原子并发槽、最长等待时间、客户端取消传递
- 401/403/429/5xx 与网络错误自动换号
- 连续失败熔断和自动恢复
- 模型别名与上游模型重写
- 管理页面、账号增删改、实时状态和最近 200 条请求
- Sub2API/OpenAI 风格 `Authorization: Bearer` 与 `X-Api-Key` 接口
- 托管客户端 Key：模型白名单、RPM、并发、过期、禁用、撤销和使用统计
- Key 热路径使用 O(1) 内存快照、SHA-256 摘要和原子限流，不访问磁盘或数据库
- 管理 Token 与客户端 Key 分离；管理页面使用短期 HttpOnly Cookie、CSRF 和登录防爆破
- 上游密钥支持环境变量，配置文件强制以 `0600` 原子写入
- 限制请求体、过滤敏感与 hop-by-hop Header、禁止上游重定向
- 入口总并发保护、慢请求读取保护和流空闲超时
- 单 Go 二进制，无第三方 Go 依赖
- AtomCode、Grok2API、Gemini Web 采用故障隔离的外置渠道适配器

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

## 调用

```bash
curl http://127.0.0.1:45679/v1/chat/completions \
  -H "Authorization: Bearer $LITE2API_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-fast","messages":[{"role":"user","content":"ping"}],"stream":true}'
```

模型别名通过 `routes` 切换。例如客户端一直使用 `deepseek-fast`，只需修改该路由的 `accounts` 或 `upstream_model`，客户端配置不用改变。

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

## 批量导入账号

管理页的“批量导入”支持同时选择或拖入多个 JSON 文件。浏览器会逐文件校验并合并，随后可先执行预检查，再确认一次性保存并热加载。单次最多导入 500 个账号，请求体上限为 1 MiB。

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

Sub2API 的 API Key 账号和代理会映射到 Lite2API。OAuth、Cookie、refresh token 等凭据不会被误当作 API Key；这类账号必须先通过外部适配器暴露 OpenAI 或 Anthropic 兼容的 `base_url`。工作流参考来源见 [Third-Party Notices](THIRD_PARTY_NOTICES.md)。

## 安全边界

- 网关没有配置 API Key 时默认拒绝所有模型请求。
- 管理 Token 没有配置时默认关闭管理 API。
- 管理页和管理 API 同时受 Nginx 来源白名单与应用层可信代理/CIDR 校验保护；伪造 `X-Real-IP` 无效。
- 浏览器只在登录请求中提交管理 Token；服务端签发短期、`HttpOnly`、`Secure`、`SameSite=Strict` 会话，写操作另需 CSRF Token。
- 托管客户端 Key 的明文只显示一次；磁盘仅保存 SHA-256 摘要，撤销通过原子内存快照立即生效。
- 建议所有上游密钥只通过 `.env` 提供。
- HTTP 上游默认仅允许 loopback；AtomCode2Api 等同机服务需要显式开启 `allow_private_http_upstream`。
- 生产入口必须使用 HTTPS，并限制 `/admin` 的来源 IP 或仅通过 VPN 访问。

## 与完整 Sub2API 的取舍

Lite2API 只面向单机、单管理员。单机默认使用内存 Key/限流状态，避免 Redis 网络往返并减少故障点。未来只有在多实例部署时才需要接入 Redis 来同步全局 RPM、并发租约和撤销事件。

它不提供用户余额、套餐计费、OAuth 登录流程、支付、团队权限和复杂协议转换。这些能力如果以后确实需要，应作为独立 Provider 或模块增加，而不是重新引入整套 SaaS 架构。

详细设计与运维说明见 [Architecture](docs/ARCHITECTURE.md) 和 [Operations](docs/OPERATIONS.md)。

## 可选渠道

Grok2API 与 Gemini Web 已作为固定提交的 Git 子模块接入，并使用独立 Compose profile，不会增加 Lite2API 核心镜像的体积。初始化、启动、账号导入和升级流程见 [channels/README.md](channels/README.md)。
