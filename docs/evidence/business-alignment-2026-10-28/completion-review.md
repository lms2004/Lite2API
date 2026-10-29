# Lite2API 调用完成子任务交付记录

## 主责与范围

主责 Agent: /root/completion。代码位于 internal/gateway/gateway.go、response_completion.go；测试位于 gateway_test.go、response_completion_test.go。permissions Agent 独立提供 completion_review_test.go 并完成有限专项审查；routing Agent 提供 request requirements 与 operation rewrite helper（其具体证据由该 Agent 负责）。未访问生产网关、上游账号或浏览器；未部署。

## 原始依据与偏差

用户目标原文：附件“已粘贴的文本.txt”，调用服务/完成调用要求区分HTTP请求、响应开始、传输结束、逻辑完成；故障处理禁止不确定重放及跨来源拼接。原 gateway.go 在 HTTP<400 且 streamResponse 读到 EOF 后直接 reportSuccess，无法区分 HTML 登录页、200 JSON 错误、截断/未完成 JSON、SSE error 或缺终止事件；这同时污染客户端完成判断、Key 统计、账号成功与路由健康。

另两项依赖偏差：原 rewrite none 删除参数而非明确关闭推理（已纠正）；原 header copy 未排除管理凭据（已加 x-admin-token/x-csrf-token）。routing Agent 发现的 Responses nested reasoning、特性筛选及契约冲突校验已接到请求路径。

## 实现与完成契约

- 普通响应先校验 JSON 语法与协议完成字段。Chat: 非空 choices 且每个 choice 存在 message 对象和非空 finish_reason；Responses: status=completed 且 output 数组；Anthropic Messages: content 数组+非空 stop_reason；count_tokens: 非负整型 input_tokens；Embeddings: data 中有 embedding 数组的项；Images: data 中有非空 URL/base64 的项；Rerank: results 数组（允许空结果）。任何顶层 error 非 null 均失败。
- 小于等于64KiB的普通响应在验证通过后提交，失败返回502 invalid_upstream_response。大响应持续转发并增量验证，不缓存长正文/base64；已提交后才发现不完整时以 http.ErrAbortHandler 中止 HTTP，客户端读到异常结束，记录 incomplete_response，不重放。转发校验响应删除 Content-Length，避免上游已声明完整长度掩盖协议不完整；普通 JSON 输出明确 Content-Type=application/json。
- SSE 按完整事件解析（换行/空行，支持CRLF、多行data、注释）。Chat要求所有已出现choice都有finish_reason，并收到完整[DONE]帧；Responses要求 response.completed 的 response.status=completed 且 output 数组；Anthropic要求 message_start（message/content）、message_delta（stop_reason）、message_stop。终止事件送达后无需等待连接EOF。200 error事件、失败/未完成事件、缺终止事件、非法JSON事件和流模式不匹配均失败，并向已开始的流发送不含上游原始错误的error事件。
- ordinary JSON 增量扫描仅保存512字节短标记/字段、128层有界栈；SSE单行/事件上限1MiB，choice索引0..127。完成相关字段重复直接拒绝；结构路径与含点号/方括号的扩展字段名隔离。跨协议完成字段不能互相冒充。
- 由Go Transport自行协商gzip并解压，避免透传客户端Accept-Encoding导致检查压缩二进制。未增加后台网络探测。
- 所有完成失败均不更换来源重发，所有已开始流均保持单一来源；客户端取消/下游写失败不影响上游熔断。
- 每次实际HTTP尝试（含OAuth刷新后的重试）通过observer关联到逻辑调用 UpstreamAttempt，具体落盘结构由主 Agent 维护。

## 验证证据

已通过：
1. `GOTOOLCHAIN=local go test ./internal/gateway -run 'TestCompletion|TestSSECompletion|TestLargeJSONCompletion|TestGatewayRejectsIncomplete|TestGatewayReportsSSE|TestGatewayLargeJSON|TestStreamTerminal|TestTinyChunked|TestStreamingHolds|TestRewriteRequest' -count=1 -timeout=30s -v`，覆盖普通和流协议、实际HTTP失败可见、不重放、大JSON传输、终止不等EOF及并发槽释放。
2. permissions独立复现 `TestCompletionReview*` 覆盖字段路径伪造、重复字段、跨协议完成、缺payload、孤立终止及响应格式偏差；修复后全部通过。
3. `GOTOOLCHAIN=local go test ./internal/gateway -count=1 -timeout=60s`：全部通过，耗时10.151s，原始输出 `/tmp/lite2api-completion-full-test-current.log`。该轮已包含gzip解码和声明Content-Length的大JSON截断真实HTTP测试。最后在其后增加MIME精确识别、普通JSON规范Content-Type及大图像不缓存测试，受影响定向复验由最终主线统一检查复用。
4. `git diff --check` 通过。

HTTP测试使用隔离临时config与loopback httptest.NewServer。首次受限沙箱禁止listen，按工具权限允许仅隔离测试后执行。未用外部真实账号证明调用可用，不能称真实客户端/上游运行验收完成。

## 保留边界

上述是网关完成/交付契约检查，不保证模型回答质量或工具最终任务成功；不做任意协议转换。SSE暂限已实现Chat/Responses/Anthropic事件契约；其他操作stream显式失败，超过1MiB单事件或128 choice索引也显式失败。普通JSON语法深度128、短数字标记512字节；超出检查边界失败，不把证据不足伪装成功。未扩大当前网关支持端点。

## 最终增量复验

- MIME精确识别、普通JSON Content-Type规范化及大图像/base64有界扫描增加后：`go test ./internal/gateway -run 'TestCompletion|TestSSECompletion|TestLargeJSONCompletion|TestLargeImageCompletion' -count=1 -timeout=30s` 通过（0.283s）。
- 主线全仓-race唯一失败来自既有异步Body.Close测试1秒调度等待在race+CPU并行下不足。将该测试的调度等待改5秒，保留全部业务/内存所有权断言，并增加sync.Once/defer确保失败时释放transport。该等待不是业务超时。
- 在任何响应头之前处理初始完成检查错误（特别是请求普通JSON/返回SSE或请求SSE/返回JSON），关闭上游Body并返回502。
- `go test -race ./internal/gateway -run 'TestTransportErrorRetainsAttemptLeaseUntilAsynchronousBodyClose|TestGatewayRejectsResponseModeMismatchBeforeHeaders' -count=1 -timeout=30s` 通过（2.399s）。

## 实际运行验收反馈后的兼容修复

主线隔离新构建实际调用发现Responses SSE失败，而Chat/Responses普通/Messages/Claude Code其余验收完成。限定源码核查发现统一SSE envelope把顶层delta固定为Anthropic对象；CLIProxy Gemini/Antigravity Responses translator实际在 `third_party/cliproxyapi/internal/translator/gemini/openai/responses/gemini_openai-responses_response.go` 生成 `response.output_text.delta` / `response.reasoning_summary_text.delta` / `response.function_call_arguments.delta`，其delta为字符串，标准合法JSON被结构体解码误报invalid JSON event。

新增 `response_delta_compat_test.go` 在修复前确证三种delta都错误失败；修复改Delta为json.RawMessage并在协议/事件分派后仅按Anthropic message_delta解对象。没有改终态输出、完整事件或失败条件。新增三种Responses delta的完成/缺终态分支、实际HTTP交付不重放、Anthropic错误字符串delta仍拒绝等验证。

`go test -race ./internal/gateway -run 'TestResponsesSSEStringDeltas|TestGatewayResponsesStringDelta|TestMessagesSSERejectsStringDelta|TestSSECompletion|TestCompletionReview' -count=1 -timeout=30s` 通过（1.975s），`git diff --check`通过。源码推断与隔离fixture已证实；主线负责重建后的实际上游复验。
