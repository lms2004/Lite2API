# Lite2API 管理台 UI/UX 全量审计与 v12 改造

日期：2026-08-20
范围：运行总览、账号管理、API 密钥与客户端、适配器目录、模型路由、提示词注入检测、账号接入/导入/导出弹窗，以及桌面与移动端导航。

## 结论

本轮不是单页换色，而是把散落在 Native v5–v10 和旧 `index.html` 中的视觉规则收口为一套最终控制台设计。业务 API、DOM 合约和原生 JavaScript 工作流保持不变；页面默认使用参考稿方向的深海军蓝主题，并继续允许用户切换浅色或跟随系统。

审计确认的两个功能性展示问题也已修复：

1. 首次打开带页面 hash 的直达链接会因 UI build 变化被重置到总览，导致 `#routes`、`#accounts` 等入口不可靠。
2. 移动端隐藏“更多”导航后，适配器与注入检测没有可见入口。

现在所有六个一级功能都直接出现在侧栏；移动端以六项同屏底栏呈现，直达 hash 无条件遵循有效页面名。

## 设计基线

参考稿中值得保留的规则：

- 深海军蓝画布与侧栏，面板依靠细边框建立层级，不堆叠玻璃、发光和大阴影。
- 绿色只表达健康、可用和主操作；蓝色表达选择、链接与中性信息；黄色和红色只表达风险。
- 页面标题、运行状态、筛选、核心指标、主表格按固定顺序阅读。
- 控制台使用紧凑的 8–10 px 圆角和较高信息密度，避免文档站式大留白。
- 桌面端尽量一屏形成判断闭环，移动端改为单轴阅读而非缩小桌面布局。

Native v12 将这些规则实现为最终设计令牌，并覆盖此前各代样式的冲突值。

## 页面审计

| 页面 | 功能与实现核查 | v12 展示处理 |
|---|---|---|
| 运行总览 | 调用次数、成功率、P95、故障切换、真实额度窗口、三次直连质量测试、双趋势图、分渠道用量、请求证据均有真实数据源或明确未知态 | 四项指标改为独立有边界卡片；额度与质量形成首屏双栏；趋势、渠道与证据统一表头、密度和空状态；标题恢复“运行总览”语义 |
| 账号管理 | OAuth/设备授权、Web/Cookie 指引、API 连接、搜索筛选、额度状态、批量导入、导出、编辑、删除和保存前连接测试均保留 | 五项账号概况改为参考稿式指标卡；“认证账号/API 连接”改为顶部明确分段；列表、额度、状态和操作使用同一颜色与行高；空状态不伪造额度 |
| API 密钥与客户端 | 三种安全预设、一次性明文、复制、连接验证、客户端命令生成、Base URL、Key 列表、撤销和高级限制均保留 | 客户端选择、配置参数和代码预览形成一个连续工作区；端点与 Key 列表使用相同面板语言；主操作统一为绿色 |
| 适配器目录 | 状态 chips、文本搜索、状态筛选、协议/操作、认证方式、运行状态、许可和外部详情均从适配器数据渲染 | 修复详细样式未被最终嵌入的问题；筛选工具栏、四列能力卡、标签/值事实区和卡片底部动作全部进入统一设计层 |
| 模型路由 | 别名列表、逻辑模型选择、推理强度、运行模式、真实上游顺序、健康状态、增删排序、JSON 高级编辑、脏状态与热加载保存均保留 | 左侧别名 master 与右侧目标链采用同一工作台边界；选中态、健康圆点、意图区和 fallback 行收紧；保存状态和危险操作仍保持分离 |
| 提示词注入检测 | 指定渠道/模型、Temperature、最大输出、八类探针、多轮会话、Token 增量、延迟、结束原因和 ASCII 解码均保留 | 修复该页只剩未样式化骨架的问题；控制区、探针列表、对话区、响应检查器和移动端表单全部重建为完整组件 |

## 全局一致性

- 导航名称统一为“运行总览、账号管理、API 密钥、适配器、模型路由、注入检测”。
- 默认主题改为深色；已有用户主题偏好继续优先，主题切换仍可用。
- 页标题固定为 24 px 左右，说明文字和英文 eyebrow 使用一致间距。
- 面板、表格、表单、筛选 chips、状态 badge、弹窗和代码区统一边框、圆角与色彩语义。
- 所有页面最大内容宽度一致；桌面端侧栏 208 px，中等宽度折叠为图标栏，手机端变为底部导航。
- 390 px 移动端下，指标双列、账号工具单列、安全检测表单渐进重排，六个一级入口同屏可达。

## Apple 交互理念调研与动效规格

本轮进一步以 Apple Human Interface Guidelines 的 Motion、Accessibility，及 WWDC18《Designing Fluid Interfaces》为一手依据。这里借鉴的是交互原则，不是照搬 iOS 的材质或装饰：管理台仍然优先服务高密度阅读、鼠标、键盘与长时间运行监控。

### 不是“苹果风”，而是以人为中心的决策框架

截至 2026-08-20，Apple 在 HIG 中把设计原则重新明确为 Purpose、Agency、Responsibility、Familiarity、Flexibility、Simplicity、Craft 与 Delight。它们不是一组圆角、毛玻璃和缓动参数，而是处理冲突时的优先级：先确认产品为什么存在、用户要完成什么，再决定导航、信息、反馈和动效。

| Apple 原则 | 交互含义 | Lite2API 的落地 |
|---|---|---|
| Purpose / 目的 | 先找出用户真正重视的结果，把核心任务做到最好 | 总览先回答“限制在哪里、调用是否正常、哪里需要处理”，不先展示装饰或实现细节 |
| Agency / 自主 | 让人自由探索、清楚知道系统正在做什么，并能退出、取消或从错误恢复 | 一级页面可随时互相切换；弹窗可关闭且恢复触发点焦点；危险动作独立确认；动效不锁住输入 |
| Responsibility / 责任 | 透明、安全、保护隐私，不隐瞒采集、权限与副作用 | 不伪造额度和趋势；质量测试明确提示会产生少量真实调用；密钥只显示一次；敏感凭据继续隔离 |
| Familiarity / 熟悉 | 建立在既有物理与数字习惯上，并保持外观、行为和反馈一致 | 侧栏、底部导航、分段控件、表格、弹窗和进度反馈使用熟悉模式；相同控件在六页保持同一行为 |
| Flexibility / 灵活 | 适配不同屏幕、输入方式、偏好与能力，同时保留上下文 | 桌面侧栏、中屏图标栏、移动底栏渐进适配；指针、键盘、触摸均可操作；支持浅色、深色与 Reduce Motion |
| Simplicity / 简洁 | 简洁不是删到空，而是只保留有任务价值的内容并建立明确层级 | 主判断在前、证据在后；高级设置渐进披露；文案直接说明状态、原因和下一步 |
| Craft / 工艺 | 每个细节都要精确，包括视觉、文字、动画与边界情况 | 统一设计令牌、焦点环、真实空态、语义色、可读小字号、单调图表曲线和完整响应式布局 |
| Delight / 愉悦 | 愉悦来自轻松、可信和恰当的人性化反馈，不来自表演 | 即时按压反馈、短促页面过渡、连续图表切换和可重定向交互共同形成“丝滑感” |

### 跨原则的 Apple 交互方法

1. **内容与任务优先。** 结构必须先让人知道自己在哪里、能做什么、下一步是什么。主要信息按阅读顺序靠前；次要事实通过分区、折叠和详情渐进披露。工具栏只放当前页面的高频动作，不能把所有功能都堆在首屏。
2. **层级、和谐与一致性。** 控件层与内容层要可辨，但不能遮住内容。对 Lite2API 而言，细边框、表面明度和留白足以表达层级；Liquid Glass 只启发顶部控制层的轻量分离，不应铺到数据卡片和表格里。
3. **反馈保持控制感。** 按下、选择、加载、完成和错误都要有及时、成比例的反馈。已知进度优先显示确定进度；未知耗时才使用不确定状态。错误需说明问题和可行动的下一步，不能只变红。
4. **响应先于动画。** 控件在按下时立即确认输入，业务请求则用独立状态继续反馈。界面不能等动画结束后才重新接收操作，也不能把动画当作状态本身。
5. **空间连续且方向对称。** 状态之间的移动应帮助理解来源与去向；打开与关闭、进入与退出应保持相反但一致的方向。没有空间含义时，淡入淡出比大范围平移更合适。
6. **连续、可打断、可重定向。** WWDC18 将流畅界面描述为与用户持续对话的行为系统。新输入要从当前视觉状态自然接管，而不是先完成旧动画；指针进入图表时会直接终止绘制入场动画并切到精确读数。
7. **符合预期的物理感。** 点击没有方向动量，因此工具型界面默认高阻尼、无过冲；只有手势本身携带速度且回弹能解释边界时才使用弹性。管理台不使用循环呼吸、视差或装饰性弹跳。
8. **短促、精确、低打扰。** 高频动作不应反复要求用户观看完整动画。运动距离越大，越容易抢走对内容的注意；因此页面只移动 6 px，控件反馈主要改变颜色、边框和极小缩放。
9. **输入方式平等。** 熟悉的鼠标悬停不能成为唯一入口。趋势分段支持左右方向键、Home 和 End；图表支持左右键逐点读取、Escape 退出；焦点位置和可见焦点环始终保留。
10. **可访问性从一开始进入规格。** 状态不只靠颜色表达；文字与背景保持足够对比；小字不能为了“精致”而失去可读性；重要信息不只靠运动传达。Reduce Motion 开启时移除位移、缩放、模糊过渡与重复动画，图表直接绘制终态。
11. **适配环境而不是缩小桌面。** 移动端改变导航和阅读轴，触控目标保持可点击空间；中等宽度折叠侧栏文字但保留图标与焦点；深浅主题使用语义令牌分别设计，而不是简单颜色反转。
12. **愉悦是结果，不是图层。** 当界面响应及时、行为可预测、错误可恢复、内容可信时，用户自然会觉得快、顺和“像自己的一部分”。这比额外毛玻璃、发光或长缓动更接近 Apple 所说的 Delight。

### 明确不照搬的部分

- 不把 Liquid Glass 当作内容容器；它适合抬升导航和控制，不适合覆盖高密度表格、额度和请求证据。
- 不为了“苹果感”增加大圆角、大留白或移动端式低信息密度；Lite2API 是桌面优先的运维工具。
- 不用动画隐藏等待，不让任何高频操作被过渡时长阻塞，也不用自动循环动效制造“活着”的假象。
- 不用平滑曲线美化出不存在的数据：缺失采样继续断开，真实值不被过冲改写，空态明确写“无数据”。

据此，Native v12 的实现规格为：

- 按压反馈 70–140 ms；悬停与颜色反馈 140 ms；页面、指标切换 240 ms；图表首次绘制或真实数据变化 360 ms。
- 主曲线使用 Fritsch–Carlson 单调三次插值：圆滑转折但不制造超过原始采样值的新峰谷；空采样仍保持断线，避免伪造连续可用性。
- 图表删除常驻密集圆点，改为悬停十字线、精确值和时间提示；原始聚合值、峰值与失败值保持不变。
- 导航、按钮和分段控件按下即反馈，页面只移动 6 px，不使用弹跳、视差、循环呼吸或大面积缩放；趋势切换保留两个面板的连续状态，不通过 `display: none` 硬切。
- 图表与趋势分段均支持键盘；指针或键盘开始探索图表时会取消尚未完成的入场动画，窗口缩放只重绘终态，不重复表演。
- 8–9 px 的关键微型文字提升到 9–10 px，并保留清楚焦点环；屏幕阅读器通过独立 live region 获得键盘选中的时间点与数值，指针移动不会造成播报轰炸。
- `prefers-reduced-motion: reduce` 下，图表直接绘制终态，所有非必要动画缩短到近乎即时，平滑滚动回退为直接定位。

这些时长是 Lite2API 根据操作频率和信息密度制定的产品规格，不是 Apple 官方规定的固定毫秒值。Apple 给出的是“短促、精确、可打断、符合输入动量并尊重 Reduce Motion”的判断标准。

参考资料：

- Apple Human Interface Guidelines — Motion: <https://developer.apple.com/design/human-interface-guidelines/motion>
- Apple Human Interface Guidelines — Design principles: <https://developer.apple.com/design/human-interface-guidelines/design-principles>
- Apple Human Interface Guidelines — Accessibility: <https://developer.apple.com/design/human-interface-guidelines/accessibility>
- Apple Human Interface Guidelines — Layout: <https://developer.apple.com/design/human-interface-guidelines/layout>
- Apple Human Interface Guidelines — Materials: <https://developer.apple.com/design/human-interface-guidelines/materials>
- Apple Human Interface Guidelines — Color: <https://developer.apple.com/design/human-interface-guidelines/color>
- Apple Human Interface Guidelines — Dark Mode: <https://developer.apple.com/design/human-interface-guidelines/dark-mode>
- Apple Human Interface Guidelines — Typography: <https://developer.apple.com/design/human-interface-guidelines/typography>
- Apple Human Interface Guidelines — Sidebars: <https://developer.apple.com/design/human-interface-guidelines/sidebars>
- Apple Human Interface Guidelines — Progress indicators: <https://developer.apple.com/design/human-interface-guidelines/progress-indicators>
- Apple WWDC18 — Designing Fluid Interfaces: <https://developer.apple.com/videos/play/wwdc2018/803/>
- Apple WWDC25 — Design foundations from idea to interface: <https://developer.apple.com/videos/play/wwdc2025/359/>
- Apple WWDC25 — Meet Liquid Glass: <https://developer.apple.com/videos/play/wwdc2025/219/>

## 验证覆盖

- Go 嵌入结构测试：最终页面仍为单一 `<style>`、单一文档闭合结构，所有业务 ID 和 handler 保持存在。
- JavaScript 语法：Native v5/v6/v7/v9/v10 与主题脚本通过 `node --check`。
- 配置与构建：隔离预览配置通过 `-check-config`，新二进制成功构建。
- 真实浏览器：在隔离的 Lite2API 实例上检查 1440×1000 桌面端的六个功能页，以及 390×844 的总览、账号和注入检测页。
- 状态覆盖：同时检查无额度数据、无 Key、无请求趋势、已配置路由、已收录适配器等真实空态与有数据状态。

## 数据边界

本轮保证“已实现能力被正确、清晰地展示”，但不会伪造后端没有的数据。额度仍依赖上游适配器返回真实窗口；趋势仍受本地保留期限制；质量测试会产生少量真实调用；适配器的“可承载流量”只有在运行探针和配置均满足时才显示为可用。

## 2026-08-21 增量优化

- 模型路由管理端新增旧式直连兼容层：`capabilities[]` 仍是首选路径，但只有 `models`、`model_map`、`targets[].model` 或旧 `accounts/upstream_model` 的配置不会再被 UI 归一化丢失。
- 路由保存会按目标能力自动选择新式 capability payload 或旧式 target-level `model/reasoning_effort` payload，保持现有 `/v1/*` 网关转发路径不变。
- 模型选择弹窗、路由编辑器和客户端配置示例共用同一模型来源：显式路由优先，其次是 capability、账号模型、model_map 与当前直连目标。
- 适配器目录将“目录/安装”“运行/鉴权”“流量”拆成三格状态，避免把仅收录、已安装、已配置和真正承载流量混为一谈。
- 新增 `native-route-compat.js`、`native-render-perf.js` 与 `native-adapter-clarity.js` 三个独立增强层，减少继续修改旧 `index.html` 单行脚本的风险。
- 管理页 HTML 和 `/admin/api/*` JSON 支持 gzip 响应；页面本体在启动时预压缩，避免每次请求重复压缩。
- 管理端渲染改为只刷新当前视图，隐藏页保留上一次 DOM；Native v10 的监控同步也收敛到运行总览激活时执行。
- `/state` 复用 runtime 缓存的模型列表，并用手写深拷贝替代 JSON 往返打码，减少轮询时的 CPU 和分配。
- `/v1/*` 请求体在无需改写模型或推理强度时复用原始 body，保留原协议语义并减少直连请求的 JSON marshal 成本。
