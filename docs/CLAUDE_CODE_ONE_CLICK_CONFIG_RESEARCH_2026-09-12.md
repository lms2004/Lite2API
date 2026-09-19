# Claude Code 一键接入配置深度研究

更新时间：2026-09-12

本文针对 Lite2API 当前的客户端配置生成器，以及 `https://sub2api.foresights.top` 的现有反向代理拓扑。目标不是堆砌环境变量，而是让“创建 Key → 明文自动填入临时命令 → 无模型调用预检 → 启动 Claude Code”成为一条可诊断、可升级的路径。

## 结论先行

当前部署应使用下面的公网基地址：

```text
https://sub2api.foresights.top/lite
```

这里故意不带 `/v1`。Claude Code 通过 `ANTHROPIC_BASE_URL` 使用 Anthropic Messages 协议，并自行请求 `/v1/messages`；因此写成域名根会命中不存在的 `/v1/messages`，写成 `/lite/v1` 又会重复成 `/lite/v1/v1/messages`。

已使用 Claude Code `2.1.269` 对公网网关执行真实验收：`GET /lite/v1/models` 返回 200，使用默认工具集的 `POST /lite/v1/messages` 首次请求返回 200，模型路由仍为 `Shadow`。

`ANTHROPIC_AUTH_TOKEN` 的值应是裸 Key，不要手动加 `Bearer `。Claude Code 会把它作为 `Authorization: Bearer <value>` 发送。`ANTHROPIC_API_KEY` 是另一条 `X-Api-Key` 认证路径，不应与本网关的 Bearer 配置同时生成。

`Shadow` 不是 Claude Code 内置模型 ID。仅设置 `ANTHROPIC_MODEL=Shadow` 会触发 `unrecognized_model`，且在 Claude Code `2.1.269` 上可以构造出被上游拒绝的请求，错误为 `role 'system' is not supported on this model`。临时命令应通过 `--settings` 添加仅本会话生效的 `modelPicker` 行，以 `behavesAs: claude-opus-4-6` 声明 Shadow 的实际能力族。这会保留 `Shadow` 路由名，同时让 Claude Code 按已知 Opus 4.6 模型生成请求。

## 推荐的一次性安全启动命令

这是当前生成器输出的 Bash/Zsh 结构。管理端创建 Key 后会把 `<AUTO_FILLED_KEY>` 直接替换为本次仅显示一次的明文，无需再次复制 Key。外层子 Shell 保证关闭 Claude Code 后环境变量自动清理。

```bash
(
# Claude Code → Lite2API（Bash / Zsh）
# Base URL 故意不包含 /v1；Claude Code 会自动请求 /v1/messages。
# 下面是文档示意；管理端生成的实际命令会自动填入 Key。
unset ANTHROPIC_API_KEY ANTHROPIC_API_HOST CLAUDE_CODE_API_BASE_URL
unset CLAUDE_CODE_USE_BEDROCK CLAUDE_CODE_USE_VERTEX CLAUDE_CODE_USE_FOUNDRY CLAUDE_CODE_USE_ANTHROPIC_AWS
export ANTHROPIC_BASE_URL='https://sub2api.foresights.top/lite'
export ANTHROPIC_AUTH_TOKEN='<AUTO_FILLED_KEY>'
export ANTHROPIC_DEFAULT_OPUS_MODEL='Shadow'
export ANTHROPIC_DEFAULT_SONNET_MODEL='Shadow'
export ANTHROPIC_DEFAULT_HAIKU_MODEL='Shadow'
export CLAUDE_CODE_SUBAGENT_MODEL='Shadow'
lite2api_claude_settings='{"modelPicker":{"options":[{"model":"Shadow","label":"Shadow (Lite2API)","description":"Lite2API model route","behavesAs":"claude-opus-4-6"}],"replaceBuiltInOptions":true}}'

if ! command -v claude >/dev/null 2>&1; then
  printf '%s\n' '找不到 claude 命令，请先安装或更新 Claude Code。' >&2
elif curl -fsS --connect-timeout 5 --max-time 15 \
  --config <(printf 'header = "Authorization: Bearer %s"\n' "$ANTHROPIC_AUTH_TOKEN") \
  "$ANTHROPIC_BASE_URL/v1/models?limit=1000" >/dev/null; then
  claude --settings "$lite2api_claude_settings" --model 'Shadow'
else
  printf '%s\n' '网关验证失败，未启动 Claude Code。' >&2
fi
)
```

`--model Shadow` 是主会话的唯一选择源，因此不再重复导出 `ANTHROPIC_MODEL`。三个 `ANTHROPIC_DEFAULT_*_MODEL` 分别覆盖 Claude Code 可能使用的 Opus/Sonnet/Haiku 内部入口，`CLAUDE_CODE_SUBAGENT_MODEL` 覆盖子代理，它们不是主模型的重复设置。

## 官方协议约束与配置决策

| 配置面 | 官方行为 | Lite2API 的决策 |
| --- | --- | --- |
| 基地址 | `ANTHROPIC_BASE_URL` 指向网关基地址；Messages 请求落在 `/v1/messages` | 生成器从 `/lite-admin/` 推导 `/lite`，再由 Claude Code 拼 `/v1` |
| 认证 | `ANTHROPIC_AUTH_TOKEN` 生成 Bearer；`ANTHROPIC_API_KEY` 生成 `X-Api-Key` | 只生成 Bearer 变量，不混用两套凭据 |
| 模型发现 | 网关发现只把特定 ID 加入 picker，且发现不等于能力识别 | 预检仍请求 `/v1/models`，模型能力由临时 `modelPicker.behavesAs` 明确声明 |
| 非标准模型 | `modelPicker` 可添加自定义 ID，`behavesAs` 使其继承已知模型的能力与请求行为 | 将 `Shadow` 声明为 `claude-opus-4-6`，同时用 `replaceBuiltInOptions` 避免切换到网关未开放的模型 |
| 模型默认值 | `--model` 的优先级高于 `ANTHROPIC_MODEL` 和 settings 中的 `model` | 只使用 `--model Shadow` 选择主会话，不再同时生成 `ANTHROPIC_MODEL` |
| Key 安全 | `ANTHROPIC_AUTH_TOKEN` 作为 Bearer token | 新 Key 仅显示一次并自动填入子 Shell；服务端仅保留摘要 |
| 自定义网关能力 | 需要转发 `anthropic-version`、`anthropic-beta`、工具/缓存/思考相关字段 | 默认命令不打开实验开关，把协议兼容放在网关测试与版本矩阵中 |

依据： [环境变量参考](https://code.claude.com/docs/en/env-vars)、[模型配置](https://code.claude.com/docs/en/model-config)、[网关兼容性协议](https://code.claude.com/docs/en/llm-gateway-protocol)、[网关认证](https://code.claude.com/docs/en/team)。

## 已落实到当前代码

1. `gatewayBaseFromPath()` 处理 `/lite-admin/ → /lite`、`/admin/ → /` 两种挂载方式，避免把管理路径误当成公网 API 路径。
2. Claude Code 生成器使用安全 Shell 引用、自动填入新 Key、`/v1/models` 只读预检，并通过临时 `modelPicker.behavesAs` 映射 Shadow 的 Opus 4.6 能力。
3. Codex/OpenAI/cURL 配置改用独立的 `/v1` API 基地址，避免 Claude Code 基地址修正后影响其他客户端。
4. Key 验证按钮也改用正确的公网 `/v1/models` 地址，并明确提示不会产生模型调用。
5. 配置面提供 Bash/Zsh 一次性启动、PowerShell 安全启动、用户级持久化三种模式；持久化模式使用 `apiKeyHelper`，合并并备份用户 settings，Key/helper/settings 权限分别收紧到 `0600`/`0700`/`0600`。
6. Key 验证会请求 `GET /v1/models?limit=1000`，同时检查当前选中的模型是否真的在该 Key 的可访问目录中。
7. 增加了路径、模型能力映射、Shell 转义、PowerShell/持久化生成器和 Key 边界的 Node 测试；生成文本通过 `bash -n` 检查，`internal/web` Go 测试通过。

相关实现：

- `internal/web/app-core.js`：地址推导、Shell 引用和 Claude Code 配置生成。
- `internal/web/app-clients.js`：客户端配置和 Key 验证调用。
- `internal/web/app-core.test.js`：纯函数回归测试。
- `internal/gateway/gateway.go`：Anthropic `count_tokens` 路径转发。

## 不应默认塞入命令的变量

### `CLAUDE_CODE_ALWAYS_ENABLE_EFFORT=1`

`Shadow` 这种未知 ID 可能使 Claude Code把它视作支持当前思考/effort 能力的模型。该变量会强制发送 effort；只有确认 `Shadow` 的实际后端支持对应字段时才应打开，否则会把一个可用的模型路由变成 `400`。

### `CLAUDE_CODE_ENABLE_FINE_GRAINED_TOOL_STREAMING=1`

自定义基地址下该能力默认关闭。只有 Lite2API 和最终上游完整透传对应工具流字段时才打开；否则工具参数流可能被网关或上游拒绝。

### `ENABLE_TOOL_SEARCH=true`

非 Anthropic 官方主机默认关闭 MCP tool search。只有网关确认支持并透传 `tool_reference` 时才打开；它不是普通 Claude Code 文本/工具调用的必需项。

### `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1`

应作为兼容性故障时的降级开关，而不是默认配置。它会减少实验能力，且不能解决所有 schema 不兼容，尤其不能替代对 adaptive thinking 的处理。

### `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`

可用于严格限制额外出站流量，但会牺牲自动更新、部分辅助能力和相关检查；应由用户明确选择，不应隐藏在复制命令中。

## 网关侧的下一步优先级

### P0：已具备或已修复

- `/lite/v1/models` 必须直接返回，不能依赖重定向；发现请求有 3 秒超时，公网代理应保持低延迟。
- 流式响应必须关闭 Nginx 缓冲并转发 keep-alive ping。当前 staging Nginx 已配置 `proxy_buffering off`、`proxy_request_buffering off` 和长读写超时，但仍应对长 thinking 请求做黑盒回归。
- `anthropic-version` 与 `anthropic-beta` 应按开放列表透传。当前 Lite2API 的请求头复制逻辑会透传普通 Anthropic 头，同时重新设置上游自身认证头。

### P1：已实现与建议实现

- 已增加 `POST /v1/messages/count_tokens` 的 Anthropic Messages 转发。官方说明没有该端点时 Claude Code 仍能工作，但 `/context` 只能显示基于字符的近似值；当前实现仍依赖上游 Anthropic-compatible 服务实际支持该端点。
- 建议 `/v1/models` 增加可选的 `display_name`、`description`，让 Claude Code 的 `/model` 列表不只显示裸别名；不能改变 `id`，因为 `id` 是实际路由键。
- 建议为路由暴露模型能力元数据（thinking、effort、工具、上下文窗口），未来可按路由动态生成 `behavesAs` 或精确 capability；当前 Shadow 的生产目标已确认为 Opus 4.6。
- 建立 Claude Code 版本矩阵，至少覆盖标准 Claude ID、`Shadow`、流式工具调用、adaptive thinking、上下文计数和 401/403/429/5xx 错误转发。

### P2：后续产品增强

- 增加复制后的 `/status`、`claude doctor` 诊断提示；用户可据此确认实际生效的设置源和被更高优先级配置覆盖的变量。
- 提供“撤销 Lite2API 用户级配置”的可审计操作，删除前展示 `settings.json` 备份与 helper 路径，并只删除本工具写入的字段。
- 建立跨平台 PowerShell 实机验收，覆盖 Windows PowerShell 5.1、PowerShell 7、代理证书和中文路径。

官方配置层级为 managed > CLI > local project > shared project > user；因此“写入了 `~/.claude/settings.json` 但不生效”并不一定是写入失败，也可能被项目或组织策略覆盖。参见 [设置文件与优先级](https://code.claude.com/docs/en/configuration)。

## 发布前验收清单

- [ ] 管理端页面为 `/lite-admin/` 时，生成 `ANTHROPIC_BASE_URL=https://sub2api.foresights.top/lite`。
- [ ] `/v1/models` 无 Key 为 401，正确 Key 为 200；验证按钮不调用 `/messages`。
- [ ] `Shadow` 通过临时 `modelPicker.behavesAs=claude-opus-4-6` 启动，并且 client-key 的模型白名单允许 `Shadow`。
- [ ] 启动后 `/status` 显示自定义 base URL 和网关认证，而不是复用本地登录状态。
- [ ] `/model` 只显示本次命令配置的 Shadow 行，且不再输出 `unrecognized_model`。
- [ ] 用 `claude --debug` 检查发现请求是否为 `GET /v1/models?limit=1000`，且没有重定向。
- [ ] 持久化模式写入后执行 `claude doctor`，确认 `apiKeyHelper`、用户 settings 和更高优先级配置没有冲突。
- [ ] 发送一次真实请求验证 `/v1/messages`、流式 ping、模型重写、工具调用和错误体转发。
- [ ] 在升级 Claude Code 后重新执行 `claude doctor` 与兼容性测试，不把当前版本的偶然行为当作永久协议。
