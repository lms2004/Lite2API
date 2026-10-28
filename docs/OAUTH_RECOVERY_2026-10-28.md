# AI 账号管理与 API 故障修复验收

验收时间：2026-10-28T21:01:46+08:00（Asia/Shanghai）。

## 结果与范围

最终程序已上线。Gemini 与 Shadow 的真实请求可用；公网 TLS 入口可达，未携带 Key 的公网模型请求仍返回 401。带凭据的真实调用仅通过本机回环地址验收，未向公网发送生产密钥。外部带 Key 的端到端调用没有在本轮单独实测。

用户明确选择稍后重新授权。因此，GPT/Codex、Claude 官方及一个 Antigravity 账号保留在账号池，显示 `reauth_required`；这些账号的可用性尚未恢复。未修改既有模型路由、客户端 Key、管理白名单或 Nginx 配置，也未删除账号。

## 已确认的原因

- 网关、Nginx 与适配器进程均在运行，故障主要发生在上游授权层。
- Claude 刷新令牌永久失效，持续返回 `invalid_grant`。
- Codex 导入凭据的 Access Token 已过期，Refresh Token 为空；旧适配器将没有实际更新 Token 的操作报告为刷新成功。
- 成功刷新后旧 401 的模型冷却和注册表挂起状态未清除，影响恢复。
- 上游 401/403 曾直接透传给客户端，容易被误认为客户端 API Key 失效；自动账号池的安全选号失败也未正确使用安全回退标记。

## 实现

- 仅信任与本机 OAuth 管理端点匹配的适配器；明确 401 后可按实际凭据索引刷新并最多重试一次。
- 同凭据并发刷新合并，结果缓存一分钟，等待上限 12 秒，缓存条目最多 128。超时、连接中断、普通 5xx 与已开始的流不触发该重试。
- 成功刷新只清除 401 授权故障及对应模型挂起，保留额度、付款、模型支持和其他上游故障限制。
- 永久失效的刷新令牌退出自动刷新和选号；已过期且没有刷新令牌的导入凭据在加载时即标记为需重新授权，有效的临时 Access Token 仍可使用。
- 上游授权拒绝归一为 502 `upstream_auth_failed`；适配器明确未提交的选号失败返回 503 `upstream_unavailable`。Messages 接口使用 Anthropic 错误格式；无效客户端 Key 仍为 401。
- 账号页区分需重新授权、暂时冷却和待检查，并提供重新授权按钮。
- 管理刷新结果仅包含公开索引和固定诊断文本；管理错误与上游授权错误不透传原始错误正文、文件名或认证挑战。
- 管理刷新必须明确指定账号 ID 或 `all=true`，拒绝空请求及冲突目标。
- systemd 与 Docker 安装流程同步维护第五组 OAuth 恢复补丁及 SHA-256 校验。

## 验证

- 网关 `go test -race ./...` 通过。
- 适配器 auth、SDK handlers、management 三个相关包的回归和竞态检测通过。
- 前端核心与运行时测试、JavaScript 语法、安装脚本语法、补丁应用一致性与 diff 检查通过。
- 仓库服务检查通过；两个服务仅监听回环地址，无自动重启。
- 最终真实调用结果如下，均在本机网关执行：

| 检查 | HTTP | 耗时 | 结果 |
| --- | --- | --- | --- |
| gemini_chat | 200 | 1.857 s | 通过：存在有效回答 |
| shadow_chat | 200 | 2.798 s | 通过：存在有效回答 |
| shadow_responses | 200 | 2.629 s | 通过：存在有效回答 |
| shadow_messages | 200 | 2.169 s | 通过：存在有效回答 |
| shadow_stream | 200 | 3.166 s | 通过：流完整结束 |

健康与模型目录可以证明进程响应、配置和鉴权路径，不等同于所有账号均可推理。未重新授权的账号仍会影响其专属模型路由。

## 发布与回滚

- 网关版本：`recovery-20261028-03144b3bcb3f`。
- 适配器版本：`v6.10.9-lite2api.9`，固定上游 `785b00c3127eea6aa207f1207ead8a2aa93690a3`。
- OAuth 恢复补丁 SHA-256：`068bdfeb0ffd5393fc7f6ea7d98e767bcf8a6060f2b41137ec394a8d4fdb027e`。
- 原始线上二进制备份：`/root/lite2api-recovery-build/rollback-20261028T125121Z/`。
- 最终替换前备份：`/root/lite2api-recovery-build/rollback-20261028T125856Z/`。
- 发布采用原子替换，启动或本机鉴权检查失败时自动回滚。
- 可审查变更：`/root/lite2api-recovery-build/source-change.patch`。
- 发布清单与原始脱敏验收：`/root/lite2api-recovery-build/deployment-manifest.json`、`/root/lite2api-recovery-build/verification.json`。

重新登录后，在账号管理页确认状态，再验证 GPT、Claude 与相应专属路由。不要在聊天或版本库提交 Token、Cookie 或密码。
