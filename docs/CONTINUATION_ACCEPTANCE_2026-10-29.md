# Lite2API 接续实现与实际验收

> 本文记录此前上线快照的历史验收。随后收尾修复了导入补偿、流式交付与发布验收器边界，最终源码及新证据见 [提交前收尾](FINAL_CLOSEOUT_2026-10-29.md)。本文件中的 `changes_uncommitted`、进程状态与二进制摘要均为记录当时的事实，不代表后续提交或部署状态。

记录时间：2026-10-29，Asia/Shanghai。本轮源码待办已修复并部署；验收结论限于下列实际来源、接口和客户端范围。Claude Code 工具往返仍受上游 429 阻断，不能报告全部客户端、全部来源或全部业务用例均已验收。

## 运行版本与部署

- 仓库：`/root/Lite2API`，基准提交 `9956f7749b39a1fa941a1f4acc2040e87fc51256`。沿用接手时的未提交改造，没有覆盖、提交或重建另一套工程。
- 发布版本：`continuation-20261029-b30368e8257e`。
- 二进制 SHA256：`f81c05f81d8aaa2859805f079b75e1c53a5ab044157dd968b3155aea53ea25d0`。
- 发布目录：`/root/lite2api-release-continuation-b30368e8257e`。该目录包含选定源码快照、逐文件摘要、构建命令、发布二进制和回退点；版本明确标为工作区快照，没有将 dirty 内容冒充原固定提交。
- 生产进程 PID `282086`，`active/running`，运行文件、安装文件及仓库内被 Git 忽略的构建产物摘要一致。后者的旧产物已另存，详见 [workspace-binary-sync.json](evidence/continuation-2026-10-29/workspace-binary-sync.json)。
- 仅更新现有网关二进制并停止/启动 `lite2api.service`。仍监听 `127.0.0.1:45679`；unit、环境文件、凭据和适配器二进制未变。CLIProxyAPI PID 仍为 `209931`，版本 `v6.10.9-lite2api.9`。
- 上线后完成三种接口真实调用及两项访问拒绝检查，按请求 ID 确认实际来源；结束后全局与账号活动数为 0。`/livez` 返回 200；整体 `/health`、`/readyz` 为 `unknown`，因为其他配置路由尚未观测，不能解释为所有模型已可用。

证据：[发布清单](evidence/continuation-2026-10-29/release-manifest.json)、[部署与回退点](evidence/continuation-2026-10-29/deployment-manifest.json)、[上线业务验收](evidence/continuation-2026-10-29/production-business-acceptance.json)、[最终运行状态](evidence/continuation-2026-10-29/production-final.json)。

## 实际修复

1. 验收工具的 wildcard/all-accounts 选择与 Go 的默认禁用语义一致；不再挑选无关模型，保留目标凭据和推理作用域。新增模型/操作/启用状态、未知用例、严格完成判定、失败退出码及信号/超时子进程清理回归。
2. OAuth status 在访问适配器前拒绝未知非空 provider。删除/停用凭据后按 provider 收敛模型与可用性，最后凭据消失时禁用连接；已有路由引用退出模型时保留受审阅声明并禁用受影响连接，其他 provider 继续服务。登录/导入不扩宽 provider 范围，不自动启用手工停用的连接。
3. OAuth-only 导入重新核对配置 revision，上传与配置事务串行；失败时恢复原有文件、删除本次新增文件。删除/停用也保存原文件或原状态，仅驻留内存；提交失败、响应丢失或取消时执行独立有界补偿。明确区分 `oauth_credential_change_rolled_back` 与 `oauth_credential_rollback_incomplete`。
4. 普通管理探针在占用资源、请求上游之前检查声明模型范围。路由健康使用与调度相同的 operation/model/credential 熔断作用域，legacy 路由先解析真实模型；另一接口可尝试不能让已熔断接口的旧成功记录重新就绪。
5. 优雅停机超时后强制关闭活动 HTTP 连接。新增实际双向回环 HTTP 取消/超时验证，确认上游取消、账号/托管 Key/全局/请求体/统计资源释放，以及同 Key 随后调用完成。
6. 发布验收器正确处理 Chat JSON/SSE 中可选的 `tool_calls: null`；仍拒绝不完整响应、强制工具未调用、缺失结束帧和结束后尾帧。真实 Claude 验收关闭自动重试，限制 1024 个输出 Token 并关闭思考，减少外部请求消耗。

主 Agent 阅读了实际差异及证据；OAuth 和协议改动经另一负责人独立复核。复核曾捕获 OAuth 取消竞态，修复后独立重复 20 轮通过。原失败证据保留，未改写为成功。

## 分项验收结果

| 范围 | 结果 | 可复核证据与限制 |
| --- | --- | --- |
| 完整源码回归 | Go 普通与 race 各 579 个测试通过事件，0 失败、0 测试跳过；vet、前端及部署回归通过 | [回归命令](evidence/continuation-2026-10-29/regression-final-commands.json)、[前端等本地命令](evidence/continuation-2026-10-29/local-final-commands.json)。随后仅验收工具改变，其受影响回归另行通过；最终运行时文件完全相同，见 [作用域对照](evidence/continuation-2026-10-29/runtime-source-equivalence-published.json)。 |
| 启动、HUP、重启、异常恢复、停止 | 真实隔离 systemd 验收 5/5；包括 SIGKILL 后自动恢复、无效 HUP 保持旧配置、在途请求结束及进程/监听释放 | [候选实际结果](evidence/continuation-2026-10-29/lifecycle-systemd-published-candidate.json)。最终发布仅版本及验收工具变化，运行时源码一致；生产执行了真实停止/启动及调用，未对生产流量注入 SIGKILL。 |
| 备份、升级、恢复 | 真实 age 恢复 8 项、隔离升级/回退 4 场景、Docker 生产者快照与失败恢复 1 项通过；冻结运行时另复验 3 条恢复业务路径 | [生命周期汇总](evidence/continuation-2026-10-29/lifecycle-summary.json)、[冻结运行时复验](evidence/continuation-2026-10-29/lifecycle-final-release-summary.json)。这些模型响应均为本地合成上游，不构成真实外部来源或生产灾备恢复证明。 |
| 真实 curl 接口 | Chat、Responses、Messages 各普通/流式，共 6 项完整成功 | [Responses/Messages 实际结果](evidence/continuation-2026-10-29/real-upstream-clients.json)、[Chat 修后重验](evidence/continuation-2026-10-29/real-chat-codex-recheck.json)。首份报告的 Chat 假失败保留，修复验收器后只重发对应两项。 |
| 指定来源特性 | `openai.chat` 工具两跳往返及原生图片输入完成，`completed=true/evidence_saved=true`，私有配置读回事实 | [实际来源验证](evidence/continuation-2026-10-29/real-upstream-clients.json)。只证明该接口与特性，未推定其他接口、流式工具或图片组合。 |
| Codex CLI 0.164.0 | 真实 Responses 流式工具往返完成，执行只读命令取得首次请求中没有的随机文件值，进程退出 0 | [实际客户端结果](evidence/continuation-2026-10-29/real-chat-codex-recheck.json)。使用临时目录、临时 Key 和已授权的单一来源。 |
| Claude Code 2.1.269 | **未通过**：默认配置及最小预算配置均收到真实上游 429，退出 1，未完成工具往返 | [最小预算实际失败](evidence/continuation-2026-10-29/real-claude-minimal-budget.json)。默认请求 32000 输出 Token/adaptive thinking 在纯本地确认，最小配置为 1024/关闭思考/关闭重试；不能用本地参数捕获或普通 Messages 成功替代客户端通过。 |
| 生产上线门禁 | Chat 普通、Responses 流式、Messages 普通均完成，实际来源一致；匿名和管理 Token 作为客户端均 401 | [上线验收](evidence/continuation-2026-10-29/production-business-acceptance.json)，三次调用分别只有一次网关发送。 |

真实成功范围统一为 `Shadow → cliproxy-antigravity → 08a41398ec92b336 → antigravity/claude-opus-4-6-thinking`。成功未推广到池内其他凭据或其他模型。请求结果仅保留协议完成结论、摘要、请求 ID、来源、状态和发送次数，不保存原始提示词、响应正文、Cookie 或 Token。

## 未完成项与恢复条件

- **Claude Code 实际工具调用**：需上游对该客户端请求恢复可用配额/权限，再以相同最小配置完成 Read 两跳验收。本轮普通 Messages、Codex 或探针成功不能证明该条件已满足，不再继续模型重试。
- **两个永久失效 grant**：Antigravity `1d1a1dea063ee3ab` 与 Codex `81c1278c15019737` 仍为 `reauth_required`。需通过既有登录流程重新授权，再验收具体来源、模型和接口；恢复旧令牌文件无法恢复已撤销授权。
- **Anthropic 备用凭据**：适配器记录 `permission_error`，当前不是已通过来源。需先恢复提供方账号权限，随后重新验收，不能将首选来源成功归入该凭据。
- **其他模型和特性**：未逐项验收其他配置别名、Embeddings、图片生成、Rerank、其他图片/流式工具组合或真实生产灾备。需要明确所需精确范围、有效已授权来源后恢复对应检查。未知保持未知。
- 自动禁用的 OAuth 连接需审阅退出模型与路由后显式启用；若补偿返回 `oauth_credential_rollback_incomplete`，先恢复适配器连通性及配置写入条件，再核对两端状态。

没有访问或修改 MyLife 数据，没有把 Lite2API 完成状态绑定到其他工程。未扩大公网开放范围，未变更凭据。工作区仍保留接手时和本轮的未提交修改，交付状态以 [validation-final.json](evidence/continuation-2026-10-29/validation-final.json) 及其引用证据为准。
