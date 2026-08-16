# Lite2API 管理台全量 UI/UX 审计

日期：2026-08-16  
范围：`internal/web/index.html`，覆盖运行总览、模型路由、上游账号、访问密钥、适配器、OAuth、导入导出与响应式体验。

## 1. 结论摘要

这次优化的核心不是“换一套颜色”，而是把管理台从配置表单集合改造成运营决策界面：

1. 默认入口从账号管理改为运行总览，用户先判断系统是否健康。
2. 顶层只呈现四个有决策价值的指标：请求速率、成功率、P95 延迟、并发利用率。
3. 增加系统健康结论、路由健康和最近异常，形成“发现问题 → 定位路由 → 查看请求/账号”的短路径。
4. 修复原趋势图两条曲线分别归一化、没有轴刻度导致的误读风险。
5. 路由升级为“逻辑模型 + 推理强度 + 真实渠道链”：能力矩阵自动筛选 Antigravity、Claude Code 官方、Web 代理等真实来源，用户只拖动渠道顺序；JSON 收进高级入口。
6. 保存按钮仅在存在未保存变更时出现，删除操作远离主操作，去掉重复的新增入口。
7. 视觉层收紧圆角、渐变和发光，状态颜色只承担语义，不承担装饰。

当前实现仍是单机、内存型运营面板。它足以支持个人网关的即时判断，但不是长期指标仓库；专业 SLO、成本、Token 和跨重启趋势需要后端补充数据模型。

## 2. 第一性原理

### 2.1 管理台的本质任务

页面不是为了展示所有配置，而是帮助单个运营者完成五件事：

1. 五秒内回答“现在能不能正常提供流量”。
2. 出现异常时知道影响了哪个别名、哪个账号、哪类请求。
3. 用尽量少的输入完成账号接入、密钥创建和路由变更。
4. 在保存前清楚知道修改是否尚未生效。
5. 对危险操作建立清晰边界，避免误删、误覆盖或泄露凭据。

因此，信息优先级应为：

`系统健康 > 异常与容量 > 路由 > 请求证据 > 账号与密钥配置 > 低频高级能力`

### 2.2 交互原则

- 高频任务优先点击：选择平台、模型、账号、策略，而不是手写 JSON。
- 低频复杂能力渐进披露：代理、自定义 Header、原始 JSON、批量导入导出。
- 自动完成优先：OAuth 自动轮询、Key 自动命名与复制、配置自动热加载。
- 状态必须有文字与颜色双重表达，不能只依靠色相。
- 用户只应在存在真实选择时看到按钮；状态信息不伪装成按钮。
- 单次页面跳转应保留当前视图，刷新后通过 URL hash 回到原位置。

## 3. 优化前主要问题

### P0：监控图存在误读风险

原图将请求数和延迟分别按各自最大值归一化，再绘制到同一无刻度坐标系。两条线的相对高度没有可比较含义，也无法读出真实请求数与毫秒值。

处理：改为左右双轴，左侧显示请求数，右侧显示毫秒，底部显示起止时间；图例与轴颜色保持一致。

### P0：默认页面与用户首要任务错位

原页面默认进入账号管理，运营者必须主动切换到监控页才能知道服务是否健康。

处理：默认进入运行总览；导航顺序调整为运行总览、模型路由、上游账号、访问密钥、适配器。

### P1：指标口径不清晰

原监控卡片混合累计值、瞬时值和最近 200 条样本，没有一致的观察窗口；“每分钟请求”也缺少口径说明。

处理：

| 指标 | 当前定义 | 用途 |
|---|---|---|
| 请求速率 | 最近 60 秒内的请求数 | 判断当前流量活跃度 |
| 成功率 | 最近 5 分钟；无 5 分钟样本时回退到最近记录 | 判断错误是否影响用户 |
| P95 延迟 | 与成功率相同的请求窗口 | 发现尾延迟 |
| 并发利用率 | 当前活跃请求 / 启用账号有界并发总和 | 发现容量逼近上限 |

每张卡片同时显示时间窗或分母，避免数字脱离上下文。

### P1：路由健康长期显示为“启用”

原路由行不检查候选账号是否启用或熔断，状态徽标始终是绿色“启用”。这会产生虚假的安全感。

处理：根据候选账号实时状态显示健康、降级或不可用；运行总览同步展示每个别名的候选健康数、窗口成功率和 P95。

### P1：路由操作冗余

原页头和列表底部都有新增路由按钮；保存按钮无论是否有改动都始终显示；JSON 与常用操作同级。

处理：

- 只保留列表底部一个“添加一条路由”。
- 保存按钮仅在有未保存变更时出现。
- JSON 收入“高级”菜单。
- 删除保持为行尾危险操作，不靠近保存按钮。

### P1：异常需要用户自行搜索

原页面只有请求表，没有主动汇总异常，用户需要筛选或扫描整张表。

处理：运行总览增加最近异常流，显示模型、账号、错误摘要、状态和时间；健康结论出现异常时提供单一“查看异常”动作。

### P2：视觉语言过度叠加

页面历史上存在多层 `<style>` 覆盖，颜色、圆角、阴影和渐变反复重定义，维护成本高，也容易出现局部不一致。

本轮先建立最终设计令牌与精炼层，统一实际呈现：

- 背景：近黑海军蓝。
- 面板：单层深灰蓝，不使用玻璃拟态。
- 强调色：绿色仅表示健康或主操作；蓝色表示信息；黄/红表示风险。
- 圆角：主面板收敛到 10px。
- 阴影：常规面板取消阴影，弹窗保留层级阴影。

后续应把三段历史样式机械合并为一个样式块，消除级联债务；本轮没有冒险重写所有既有组件样式，以避免覆盖正在开发的 OAuth 与适配器功能。

## 4. 新信息架构

### 4.1 运行总览

阅读顺序：

1. 系统健康结论。
2. 四个核心指标。
3. 流量与延迟趋势。
4. 路由健康与最近异常。
5. 可搜索、可筛选的请求证据表。

健康结论规则：

- 不可用：没有启用账号，或所有启用账号均不可用。
- 需要关注：存在熔断账号、窗口成功率低于 99%、P95 高于 3000ms，或并发利用率达到 80%。
- 正常：上述条件均未触发。

这些阈值是个人网关的运营默认值，不应被解释为通用 SLO。未来应允许按路由配置目标值。

### 4.2 模型路由

旧的 `accounts[] + upstream_model` 把“模型意图”和“接入渠道”混在一起；前一版把 Quality、Balanced、Fast 当成渠道，又进一步掩盖了真实凭据来源。渠道的本质应是 Antigravity、Claude Code 官方、Web 代理或独立 API 账号。仅改页面会制造能力已经存在的假象，因此本轮同步修改了配置、调度、请求重写、管理 API、CLIProxy 模型命名空间与生产配置。

新结构为：

```json
{
  "gpt": {
    "model": "claude-opus-4-6",
    "reasoning_effort": "high",
    "targets": [
      {"account": "cliproxy-antigravity"},
      {"account": "cliproxy-claude-code"}
    ]
  }
}
```

渠道自身维护能力映射，例如 `claude-opus-4-6 + high → antigravity/claude-opus-4-6-thinking`。同一逻辑组合可以映射到多个渠道的不同物理 ID；页面与服务端都以能力交集判断兼容性。

路由页现在回答五个问题：

- 客户端调用什么别名？
- 当前路由选择哪个逻辑模型和统一推理强度？
- 哪些真实渠道实际支持这个组合？
- 当前严格 fallback 顺序和健康状态是什么？

交互变化：

- 每条路由只选择一次逻辑模型和推理强度；目标行只呈现真实渠道与自动解析出的物理模型 ID。
- 改变模型或强度后，页面按渠道 `capabilities[]` 自动移除不兼容项并补入新兼容渠道；服务端再次校验，避免绕过 UI 保存错误组合。
- 支持拖动排序；键盘可用 `Alt + ↑/↓` 调整顺序。
- 添加 fallback 只列出尚未使用、已启用且支持当前模型与强度的真实渠道。
- `auto` 保留客户端的 `reasoning_effort`，`none` 移除该字段，其余强度由路由统一覆盖。
- 同一模型可以在不同渠道重复出现；运行时按目标编号排除失败项，不会把同模型的后续渠道一起跳过。
- 显式目标链不再被旧的账号数量或三次尝试上限静默截断。
- 网络错误、401/403、408/409、429、目标模型 404 和 5xx 会进入下一级；普通客户端 4xx 不扩散到所有渠道。
- 旧路由在页面中自动展开，保存后迁移到 `targets[]`；后端继续读取旧字段，避免升级中断。
- 有改动时持续显示未保存状态和保存按钮，不弹阻断式模态框。
- 运行总览的路由行可一键进入路由配置。

### 4.3 上游账号

保留现有两条接入路径：

- OAuth 快捷添加：用户只选择平台、打开授权链接、必要时粘贴回调地址。
- API Key 手动添加：平台模板自动填写 Base URL、协议、模型建议和合理默认值。

高级字段继续折叠，包括 ID、模型映射、并发、优先级、代理和请求头。这样既减少新用户填写量，也不牺牲专家能力。

### 4.4 访问密钥

默认动作仍是“一键生成并复制”：自动命名、允许全部模型、不限速、永不过期。只有需要权限收敛的用户才展开高级创建。

风险提示保留：明文只显示一次；撤销操作需要确认。

### 4.5 适配器

适配器页承担能力目录与运行发现，不与账号页重复。优先显示是否能承载流量、运行状态、操作类型、延迟和模型数；搜索覆盖平台、操作和项目。

## 5. 专业监控审计

### 已覆盖

- 请求速率。
- 窗口成功率。
- P95 尾延迟。
- 并发利用率。
- 账号启用与熔断状态。
- 路由窗口成功率、P95 和候选健康数。
- 429、4xx、5xx 与网关错误的最近异常。
- 客户端模型到实际上游模型的映射证据。
- 请求级账号、状态、耗时和错误摘要。

### 当前数据边界

`Stats` 只保存最近 512 条请求和进程生命周期累计计数；请求摘要另外异步写入有界轮转日志，因此：

- 服务重启后历史清零。
- 低流量时“最近 5 分钟”会回退到最近记录，页面会明确展示样本数。
- 无法提供小时/天/周趋势。
- 无法计算严格的时间加权 QPS。
- 已记录上游返回的输入/输出/总 Token、缓存 Token 与缓存率；未返回 usage 的渠道显示未知，不做伪造估算。
- 暂无成本、首 Token 延迟、流式持续时间和输出吞吐。
- 无按 API Key、账号、路由的长期 SLO 与错误预算。
- `failovers` 只有累计计数，没有每次切换的结构化事件链。
- 最近请求会记录最终使用的 `reasoning_effort`，但尚未持久化每次失败目标的强度与响应摘要。

### 后端优先级建议

1. P0：按分钟聚合请求数、成功数、状态族、P50/P95/P99，保留 24–72 小时。
2. P0：记录每次尝试和 failover 链，而不只记录最终账号。
3. P1：采集输入/输出 Token、首 Token 延迟、流式持续时间和取消原因。
4. P1：按别名配置成功率、P95、容量阈值和错误预算。
5. P1：输出 Prometheus/OpenTelemetry 指标，管理台只做轻量查询与可视化。
6. P2：增加成本估算；只有配置价格来源后才显示，不能伪造未知价格。

## 6. 可用性与点击成本

| 任务 | 优化前 | 优化后 |
|---|---|---|
| 判断系统健康 | 进入监控并自行解释多项数据 | 默认首页直接给出健康结论 |
| 定位异常路由 | 请求表搜索 + 切换路由页 | 最近异常/路由健康一键进入 |
| 新建普通 Key | 展开表单填写 | 一次点击生成并复制 |
| OAuth 接入 | 已是点击式流程 | 保留，继续自动轮询 |
| 设置渠道优先级 | 依赖账号全局 priority，路由内不能逐项控制 | 拖动目标链直接决定真实 fallback 顺序 |
| 选择上游模型 | 所有账号共用一个值或依赖隐藏映射 | 路由选择逻辑模型，渠道能力自动解析物理模型 |
| 设置推理强度 | 不支持 | 路由统一点击选择；候选真实渠道随能力自动适配 |
| 保存路由 | 始终存在按钮 | 仅有变更时出现 |
| 新增路由 | 两个重复入口 | 一个明确入口 |
| 编辑原始 JSON | 与高频操作同级 | 收入高级菜单 |

## 7. 无障碍与响应式

已保留或增强：

- 所有关键按钮有文字或 `aria-label`。
- 状态同时使用文字、颜色和圆点/图标。
- 支持键盘 focus 样式。
- 支持 `prefers-reduced-motion`。
- 表格在窄屏允许横向滚动，不压缩到不可读。
- 手机端导航固定底部，核心五个入口保持一跳可达。
- 健康结论和路由健康在窄屏自动降列。

仍需后续验证：

- 使用真实 Chromium + axe-core 做 WCAG 2.2 AA 自动扫描。
- VoiceOver/NVDA 验证弹窗焦点陷阱与关闭后的焦点恢复。
- 320px、768px、1024px、1440px 四档视觉回归截图。
- 图表增加隐藏的数据表或可读文本摘要，改善屏幕阅读器体验。

## 8. 视觉基准

本轮通过内置 imagegen 生成两张 UI mockup，用于校准信息层级、密度、色彩与组件比例；运行时页面仍使用原生 HTML/CSS/JS，没有把位图当作界面背景。

- `docs/design/admin-monitoring-direction.png`
- `docs/design/admin-routing-direction.png`

采用的共同约束：真实产品 UI、暗色专业运维面板、清晰 12 列网格、状态颜色克制、无玻璃拟态、无装饰插画、无重复按钮、可读中文排版。

最终 Prompt A（监控方向）：

```text
Use case: ui-mockup
Asset type: shippable desktop web admin console mockup, monitoring overview
Primary request: Redesign the Lite2API personal AI gateway operations console from first principles. The operator must understand system health in five seconds, detect anomalies, and reach the affected route or account with one click. This is a real product UI, not concept art.
Subject: restrained navigation; compact live status bar; one health verdict; request rate, success rate, P95 latency and concurrency utilization; a readable dual-series chart; route health and actionable recent incidents.
Style: practical, implementation-ready, data-dense observability UI; near-black navy and charcoal; semantic emerald, amber, red and muted blue.
Constraints: clear purpose for every control; no redundant refresh; no oversized hero; no glassmorphism, decoration, marketing cards, excessive pills, glow or watermark; accessible contrast and readable Chinese typography.
```

最终 Prompt B（路由方向）：

```text
Use case: ui-mockup
Asset type: shippable desktop web admin console mockup, model routing workspace
Primary request: Design the Lite2API routing screen from first principles. Make alias-to-upstream routing, priority order, health, fallback behavior and unsaved changes understandable at a glance; common choices should use clicks and selections instead of JSON typing.
Subject: route matrix for gpt, gemini, claude and grok; ordered targets; live health, success rate and P95; simple strategy/target controls; raw JSON under advanced disclosure.
Style: same restrained dark operations-console system; compact 40–44px targets; master-detail clarity.
Constraints: ordered fallback is unambiguous; current traffic differs from standby; persistent non-modal unsaved state; one separated destructive action; no redundant buttons, spreadsheet feel, JSON-first UI, cyberpunk glow, decorative gradients or watermark.
```

## 8.1 渠道账号与额度方向（UI build r9）

- “上游账号”改为“渠道账号”，按 Claude、Codex、Gemini CLI、Antigravity、Kimi 分组、搜索和状态筛选。
- 账号行显示脱敏身份、套餐、认证状态、成功/失败计数，以及真实响应或官方额度接口观测到的窗口；未知额度明确显示“等待观测”。
- Claude 支持 5 小时、7 天、Sonnet 周、Opus 周；Codex 自动显示主/次额度；Gemini CLI 与 Antigravity 自动显示最紧张的模型桶，反重力同时显示 AI Credits。百分比 85% 起预警、95% 起高风险，但百分比本身不改变调度。
- 路由连接管理收进折叠区，避免把一个共享认证池误解为多个 OAuth 账号。
- 全局固定 5 秒并行轮询改为页面感知刷新：账号页 15 秒、适配器页 60 秒、密钥页 30 秒，隐藏标签页退避到 60 秒。
- 额度快照只存内存：Claude 由真实请求驱动，Codex/Google 系渠道采用页面按需、10 分钟 TTL 的官方查询；不开展常驻轮询，不开启全局响应头透传，也不引入数据库或 Redis。

## 9. 验证计划与验收标准

### 本轮已完成

- Lite2API 全量 `go test ./...` 通过；CLIProxyAPI 额度、管理接口与构建目标测试通过。
- 新增真实渠道能力校验、逻辑模型到渠道模型解析、按模型与强度过滤、跨渠道同模型 fallback、目标模型 404 fallback，以及推理字段覆盖/移除测试。
- Node.js 对嵌入脚本和生产返回页面的 JavaScript 语法解析通过。
- `git diff --check` 通过。
- 候选实例在备用回环端口成功加载生产等价配置。
- 候选管理登录返回 HTTP 200，并成功签发 CSRF 会话。
- 生产服务重启后保持 `active`，健康端点返回 2 个真实渠道、4 个客户端模型。
- 生产模型目录仍为 `claude`、`gemini`、`gpt`、`grok`。
- 生产 `/health` 返回 2 个真实渠道、4 个模型，服务状态为 `active`。
- 生产管理 API 已返回四条 `targets[]` 路由，并完成一次原样 PUT 保存/热加载验证。
- 公网页面与嵌入页面均已包含 UI build `2026.08.16-r9`、能力自适配、真实渠道目标链、多渠道额度与拖动排序逻辑。

### 自动验证

- `go test ./internal/web`
- `go test ./internal/gateway ./internal/config`
- `go test ./...`
- `git diff --check`
- JavaScript 语法检查。
- Chromium 桌面与移动截图（当前主机无 Chromium，列为下一轮浏览器视觉回归项）。

### 功能验收

- 管理会话可自动建立。
- 五个视图均可切换，刷新后保留当前 hash。
- 运行总览在有/无请求、有/无账号、熔断状态下均不报错。
- 路由修改后出现保存状态；成功保存后恢复已同步。
- 切换逻辑模型或推理强度后真实渠道列表立即适配；不支持的组合在浏览器与后端均被拒绝。
- 同一模型可配置到多个渠道，顺序调整后保存的数组顺序就是运行时尝试顺序。
- OAuth、手动账号、Key、导入导出、JSON 编辑行为不回归。
- 700px 以下导航、指标、洞察面板和弹窗可用。

## 10. 后续路线图

### 下一步（高价值、低风险）

- 合并历史 CSS 为单一设计系统文件。
- 为监控图提供文本摘要与空/稀疏数据状态。
- 增加路由详情侧栏，展示真实失败切换链。
- 增加账号行内启停开关，但必须有明确保存和错误回滚状态。

### 需要后端配合

- 持久化时间序列、Token、成本和首 Token 延迟。
- 结构化路由尝试/failover 事件。
- 可配置 SLO 与告警阈值。
- Prometheus/OpenTelemetry 导出。

### 不建议加入

- 纯装饰首页、欢迎横幅或营销文案。
- 未绑定真实动作的“优化”“智能诊断”按钮。
- 无数据来源的成本、节省比例和预测指标。
- 同一操作在页头、卡片和列表尾部重复出现。
- 把所有高级配置平铺到默认表单。
