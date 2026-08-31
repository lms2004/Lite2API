# Lite2API 全栈架构彻底优化实施报告

> 日期：2026-08-30
>
> 基线：[CODE_ARCHITECTURE_DEEP_AUDIT_2026-08-30.md](CODE_ARCHITECTURE_DEEP_AUDIT_2026-08-30.md)
>
> 范围：Go 核心数据面、配置与管理控制面、适配器、管理前端、容器、systemd、Nginx、备份恢复、CI 与供应链
>
> 结论：68 项基线问题中，**56 项已修复、8 项已缓解、4 项延期**；所有 P0 已闭环，最终数据面独立复核未发现新的 P0/P1 发布阻断。

## 1. 实施结论

本轮不是局部打补丁，而是围绕五个原始根因重建关键不变量：

1. 请求只有通过真实认证、Key RPM/并发、全局并发与字节预算后才读取和解析；请求体、JSON 副本、重写副本、transport 所有权和流生命周期都有明确 owner。
2. 配置写入改为锁内最新快照事务；desired、环境 overlay、runtime state 与 observation 不再被同一次保存隐式混写。
3. scheduler、breaker、route readiness、request log 和管理会话全部具备跨 reload 的代际语义，旧请求不能污染新配置的认证或健康证据。
4. 管理前端的 XSS、会话恢复、取消、敏感字段清理、导入一致性、统计口径和批量成本上限已建立可测试契约。
5. Compose 与 systemd 统一到固定源码、固定补丁和固定镜像；安装、回滚、备份、allowlist 与 CI 从“操作说明”升级为可验证交付链。

### 1.1 状态汇总

| 范围 | 已修复 | 已缓解 | 延期 | 合计 |
|---|---:|---:|---:|---:|
| CORE | 24 | 3 | 2 | 29 |
| UI | 15 | 3 | 2 | 20 |
| DEP | 17 | 2 | 0 | 19 |
| **总计** | **56** | **8** | **4** | **68** |

状态定义：

- **已修复**：基线 finding 的具体故障模式已经闭环，并有代码或自动化回归证据。
- **已缓解**：主要风险已显著下降，但审计要求的完整状态机、数据模型或自动化证明仍未全部实现。
- **延期**：需要显著 schema、前端体系或兼容策略变更，本轮没有用高风险“大爆炸重写”伪装完成。

## 2. 关键架构变化

### 2.1 请求数据面与资源所有权

新的请求顺序为：

```text
路由/方法校验
  -> 认证与 managed-key 策略
  -> Key RPM / Key 并发
  -> 全局并发
  -> Content-Length / chunked 增量字节预算
  -> O(1) JSON 顶层字段预扫描
  -> 单次读取与解析
  -> scheduler 选择与排队
  -> 带内存租约的请求重写和 transport ownership
  -> 上游响应 / 安全 failover / uncertain submission
  -> 有界下游写与统一 observation
```

具体结果：

- middleware 不再提前缓存最高 64 MiB body；`ServeGateway` 在所有准入后单次读取。
- 已知长度预留、chunked 32 KiB 增量 CAS 计费；预算覆盖 inbound bytes、`RawMessage` 和 profile/target rewrite 的真实三份峰值。
- 在 `json.Unmarshal(map[string]RawMessage)` 前执行 O(1) 顶层扫描，提前拒绝超过 256 个字段、超长 key、超长 model/session，避免 map/key 放大先于限制发生。
- body lease 在 queue、retry 和 transport 异步 `Close` 完成前保持；进入长响应流或最终有界写回前释放。
- 上游请求显式保留 `Content-Length`，避免自定义 Body 意外转为 chunked；上传、response header、error body、stream idle 和 downstream write 均有边界。
- 401/402/403/429、显式目标 404 和可证明请求未写入的 transport error 可以换目标；408/409/425/5xx、写后 transport error 一律返回脱敏 `uncertain_submission`，不跨账号重放生成请求。
- 完整上游错误响应不再跨客户端缓存或回放。

### 2.2 调度、熔断与就绪证据

- scheduler 用跨 reload 的 generation broadcast 替代容量为 1 的共享 channel；多 waiter、旧 lease、新 scheduler 均可正确唤醒。
- lease 释放幂等；账号删除再重加通过 capacity tombstone 保持旧流占用，不可借 reload 绕过并发限制。
- breaker 按 operation + upstream model 隔离，账号身份变化重置健康；并发乱序结果使用序列围栏，旧结果不能覆盖新状态。
- wildcard breaker 使用 64 个有界 shard，避免任意模型 ID 无限扩张状态。
- readiness 不再依赖会被高流量挤出的 `Recent[512]`，而是保存每条 route 的最新真实上游证据。
- route fingerprint 覆盖路由和解析后的账号身份、endpoint、模型映射；同 alias reload 后，旧请求和旧日志的迟到证据被拒绝。
- client cancel、queue cancel、downstream timeout/write error、上游 400/422 对 route health 中性；真实上游失败才影响 readiness。
- `/livez` 与 `/readyz` 分离；冷启动、陈旧证据、无目标和当前路由失败有明确结构化状态。

### 2.3 配置、控制面与会话事务

- Upsert/Delete/Replace/Import/能力同步均在 `reloadMu` 内基于最新配置完成 read-modify-write，消除 lost update。
- 控制面记录磁盘 generation；未 reload 的外部修改返回 `ErrConfigConflict` / HTTP 409，不再静默覆盖。
- `SaveEffective` 不把环境 overlay 写回 desired JSON；持久化 rename 后 fsync 父目录。
- Config v1 使用严格 unknown-field、单一 JSON 值、duration string、资源上限和路由 schema 校验。
- `Normalize`、`Gateway.Config` 和 runtime state 深拷贝可变 map/slice。
- config、client key、request log 路径在保存和首次 `Load` 时都做 canonical/symlink 碰撞校验，启动 fail closed。
- 空 route 不再隐式 wildcard；只有 `all_accounts: true` 才是显式全账号，删除最后目标会删除 route。
- runtime commit 统一处理持久化、日志 reconfigure、restart-required 字段、transport 清理、session revoke 和 state publish；失败不留下部分副作用。
- 管理 Cookie 与认证 generation 绑定；旧 token 的阻塞登录不能在 token reload 后签发新代可用会话。

### 2.4 日志、统计与协议一致性

- `Outcome` 成为成功、失败、趋势和 readiness 的统一事实字段，避免 `ok`、HTTP status、Error 三套口径冲突。
- 所有 client-controlled observation 字段有 UTF-8 安全字节上限；request log 每行保持合法 JSON。
- request-log actor 支持同路径复用、reconfigure、drain 和 `Gateway.Close`；启动恢复在 rotate 前执行。
- 启动扫描对最高约 2.3 GiB retention 采用单遍、16,384 条最小堆和逐 route latest 聚合，内存不再与日志大小线性绑定。
- latency 每分钟最多保留 2,048 样本，P95 内存和排序复杂度有界。
- OpenAI/Anthropic 网关错误 envelope、405 `Allow`、动态 `Connection` hop-by-hop header 清理均有契约测试。
- BaseURL 固定 query 优先保留，客户端 query 不能覆盖；恶意 query 在 selection 前返回 400，不污染 breaker。

### 2.5 管理前端

- 动态质量行移除账号 ID 拼接式 inline `onclick`，改为 escaped data attribute + 事件委托，闭合持久 DOM XSS 路径。
- 统一 `requestJSON`、默认 timeout、AbortController、session single-flight，以及 401/CSRF 单次恢复。
- OAuth 使用 generation + controller；Esc、关闭、pagehide、provider 切换都会取消旧请求，旧结果不能写回新会话。
- dialog teardown 会清理 Cookie/SSO、API Key、OAuth 字段和 File 引用；新 Key 仅保留 5 分钟并支持显式清除。
- import apply 默认禁用；preview 绑定最终 data + mode SHA-256；按最终 JSON wrapper 的 UTF-8 字节数执行 1 MiB 契约。
- 删除账号前解析 canonical targets 和 legacy accounts，展示受影响 routes；最后目标采用二次高危确认。
- 趋势、quota、routing 与批测均有 generation/abort；批测最多 10 渠道、30 次调用、单次 20 秒、批次 90 秒。
- P95 不再平均 percentile；有明细时计算真实范围 P95，无明细时明确标为最新原始桶。
- prompt lab 精确限制 64 条/256 KiB、90 秒、128 KiB 响应展示，并改为增量 transcript。
- Tab/Radio 补齐 roving tabindex、方向键、Home/End；移除嵌套 `<main>` 和旧层构建号覆盖。

### 2.6 部署、恢复与供应链

- Grok 从 mutable `latest + host network` 改为 digest 固定、独立 bridge、loopback、UID 10001、read-only、cap drop 和资源限制；显式读取 `/run/grok2api/config.yaml`。
- Main、CLIProxy、Gemini 基础镜像全部固定 digest，Dockerfile 兼容 legacy builder，不依赖 `COPY --chmod`。
- Compose 与 systemd 统一 CLIProxy commit `785b00c3127eea6aa207f1207ead8a2aa93690a3`、三份 patch 与 SHA；installer 从 detached clean worktree 构建，不接受现场 dirty/untracked 冒充固定版本。
- systemd installer 创建用户、组、目录、env 和 config，严格 Go 1.26.5；目标类型/symlink 拒绝，失败自动恢复。
- rollback 快照位于 root-only `/var/lib/lite2api-rollbacks` 与 `/var/lib/cliproxyapi-rollbacks`，不再置于服务可写父目录。
- Nginx allowlist 由单一 env 来源渲染，同目录原子 `mv`；补齐 `X-Forwarded-Proto/For`。
- Compose/systemd 备份覆盖核心配置、client keys、request logs、渠道 runtime 与 Grok 卷；使用 age 流式加密、manifest/checksum、路径/type/symlink/traversal/占位密钥验证。
- 迁移脚本全量预检、拒绝覆盖、稳定 hash 文件名、hardlink create-if-absent commit、碰撞回滚并修正 owner。
- 删除自写仓库/导出源码的一次性 workflow；所有 Actions 固定完整 SHA，npm 使用 lock + `npm ci`，workflow 仅 `contents: read`。
- 新 CI 覆盖 Go race/vet、Node、ShellCheck、bootstrap/backup/migration、Compose、全部 Dockerfile、patch clean replay 和 immutable policy。
- `.dockerignore` 改为 allowlist，构建上下文不再上传 `.env`、`channels/runtime`、凭据或备份。

## 3. 68 项逐项状态矩阵

### 3.1 CORE（29 项）

| ID | 状态 | 核心证据 / 剩余边界 |
|---|---|---|
| CORE-01 | 已修复 | 删除最后目标会删除 route；空 route fail closed；显式 `all_accounts` 才 wildcard |
| CORE-02 | 已修复 | 准入后单次读 body；大 body 在 queue/retry/transport ownership 全程计费 |
| CORE-03 | 已修复 | model 256 B 与 observation 字段预算；超长 model 不进入统计环 |
| CORE-04 | 已修复 | `updateConfig` 锁内最新快照事务；并发更新和外部编辑冲突测试 |
| CORE-05 | 已修复 | generation broadcast、跨 reload 唤醒、幂等 Release、remove/re-add tombstone |
| CORE-06 | 已修复 | 删除跨请求 raw upstream response replay |
| CORE-07 | 已修复 | handler join、downstream deadline、下游错误不进入 breaker/readiness |
| CORE-08 | **已缓解** | breaker 已细分并加乱序/身份围栏；仍缺 cooldown 后 half-open 单探针状态机 |
| CORE-09 | 已修复 | environment overlay 不再落盘 |
| CORE-10 | 已修复 | request-log 单 owner、同路径复用、reconfigure、drain、Close、先恢复后 rotate |
| CORE-11 | 已修复 | canonical/legacy schema 严格互斥，运行时与校验语义一致 |
| CORE-12 | 已修复 | restart-required 字段拒绝热改；统一原子 lifecycle commit |
| CORE-13 | 已修复 | token/CIDR/TTL generation 变化撤销 session；阻塞旧登录测试 |
| CORE-14 | 已修复 | capability 保留 logical/upstream/effort 一一映射 |
| CORE-15 | 已修复 | `RequestRecord.Outcome` 统一成功、失败、趋势、readiness |
| CORE-16 | 已修复 | Normalize、clone 和 Gateway.Config 深拷贝 |
| CORE-17 | 已修复 | BaseURL 固定 query 保留且客户端不可覆盖 |
| CORE-18 | 已修复 | prompt-test 总超时、8 MiB 上限与 scheduler release 唤醒 |
| CORE-19 | 已修复 | strict schema/EOF/duration、dir fsync、Save/Load 路径碰撞 fail closed |
| CORE-20 | **已缓解** | Anthropic envelope、protocol error 和 `Allow` 已统一；尚无完整 typed domain error + central mapper |
| CORE-21 | 已修复 | `/livez`/`/readyz`、route fingerprint/latest、stale/neutral 语义 |
| CORE-22 | 已修复 | 安全 failover 与 uncertain submission 边界，生成请求不做不确定重放 |
| CORE-23 | **延期** | capability 仍不能表达 per-model operation/feature |
| CORE-24 | 已修复 | latency 样本固定 2,048，趋势内存和排序有界 |
| CORE-25 | 已修复 | 请求/响应动态 `Connection` token headers 被剥离 |
| CORE-26 | **已缓解** | discovery 可事务 rebase/身份校验；仍直接写 desired config，缺 TTL/provenance observed store |
| CORE-27 | 已修复 | adapter probe singleflight、最多 4 并行、总预算与鉴权 models contract |
| CORE-28 | **延期** | legacy env Key 仍没有 per-key model/RPM/concurrency 策略与管理告警 |
| CORE-29 | 已修复 | legacy round-robin 首请求命中第一个账号 |

### 3.2 UI（20 项）

| ID | 状态 | 核心证据 / 剩余边界 |
|---|---|---|
| UI-01 | 已修复 | 动态 inline handler 改 data attribute + 委托，结构测试禁止旧注入形态 |
| UI-02 | 已修复 | 统一 timeout/abort/session single-flight/401-CSRF 恢复 |
| UI-03 | 已修复 | 删除 impact preview、route 列表和最后目标二次确认 |
| UI-04 | 已修复 | 删除 v5—v12 叠加层，收敛为 `app.html` / `app.css` / `app-core.js` / `app.js` 单一应用 |
| UI-05 | 已修复 | 构建只接受唯一 CSS/JS slot，缺失或歧义立即 panic；不再依赖 DOM range 替换 |
| UI-06 | **已缓解** | 动态 inline handler 已全部删除，脚本由 CSP SHA-256 精确授权；自包含样式和动态进度条仍需 inline style |
| UI-07 | 已修复 | OAuth generation/controller 与完整 teardown |
| UI-08 | 已修复 | Cookie/API key/OAuth/file refs 统一清理，新 Key 5 分钟 TTL |
| UI-09 | 已修复 | preview fingerprint 绑定 data+mode，apply 默认禁用 |
| UI-10 | 已修复 | 真实范围 P95 或准确标注最新原始桶，不再平均 percentile |
| UI-11 | **已缓解** | 只渲染当前视图，轮询时保护路由、账号池与客户端焦点；活动区域的数据变化仍会局部整块替换 DOM |
| UI-12 | 已修复 | selection 与现存 ID 求交，route draft 避免双 RAF 覆盖 |
| UI-13 | 已修复 | routing/quota 失败不永久缓存，可重试 |
| UI-14 | **已缓解** | 调用数、token、单次/整批超时与取消已限制；仍缺任意子集选择和按渠道价格估算 |
| UI-15 | 已修复 | trend generation + AbortController，删除共享 busy boolean |
| UI-16 | 已修复 | 64 条/256 KiB/90 秒/128 KiB 响应与增量 transcript |
| UI-17 | 已修复 | 按最终 wrapper JSON UTF-8 body 校验 1 MiB |
| UI-18 | **已缓解** | 单一 controller + 纯 domain core，已删除跨文件 `window` override；controller 仍偏大，尚未采用 keyed renderer |
| UI-19 | 已修复 | canonical build 单点渲染，旧 v5/v6/v7 不再覆盖 |
| UI-20 | 已修复 | 移除嵌套 main，补齐 Tab/Radio 键盘语义 |

### 3.3 DEP（19 项）

| ID | 状态 | 核心证据 / 剩余边界 |
|---|---|---|
| DEP-01 | 已修复 | Grok digest/bridge/loopback/non-root/read-only/cap/resource 边界 |
| DEP-02 | 已修复 | Compose/systemd 使用同一 commit、patchset 与版本 |
| DEP-03 | 已修复 | installer 从 detached committed worktree 构建并校验 patch |
| DEP-04 | 已修复 | 根 CI 覆盖 Go/submodule/patch/Compose/Docker/scripts |
| DEP-05 | 已修复 | 两 installer 事务 snapshot、健康失败自动恢复 |
| DEP-06 | 已修复 | Compose 命名卷和镜像 rollback 流程与实际拓扑一致 |
| DEP-07 | 已修复 | HTTPS submodule、初始化、root/普通 Docker 用户 UID 契约 |
| DEP-08 | 已修复 | Lite installer 可创建新主机运行前提 |
| DEP-09 | 已修复 | Grok 命令与挂载统一 `/run/grok2api/config.yaml` |
| DEP-10 | 已修复 | 迁移预检、拒绝覆盖、原子 commit/rollback、owner 修正 |
| DEP-11 | 已修复 | `.dockerignore` allowlist 排除环境与 runtime secrets |
| DEP-12 | 已修复 | 删除 self-writing workflow；Action SHA、npm lock、read-only token |
| DEP-13 | 已修复 | Nginx/Lite allowlist 单一来源和一致性检查 |
| DEP-14 | 已修复 | CLIProxy 替代 Web UI callback 只绑定 `127.0.0.1` |
| DEP-15 | 已修复 | curl 使用 0600 header file；README 令牌不进入 curl argv |
| DEP-16 | 已修复 | CLI 健康检查验证配置 Key 和鉴权 `/v1/models` |
| DEP-17 | **已缓解** | image/action/toolchain/metadata 已固定；仍缺 SBOM、provenance、签名、apk 字节锁定和重复构建 digest 断言 |
| DEP-18 | **已缓解** | age 加密、manifest、恶意归档 verifier 已有；仍缺自动离机 retention、跨源一致性和隔离恢复演练 |
| DEP-19 | 已修复 | 删除孤立 root AtomCode unit |

## 4. 最终验证证据

### 4.1 本轮最终独立执行

| 验证 | 结果 |
|---|---|
| `GOTOOLCHAIN=local GOCACHE=/tmp/lite2api-go-cache go test -race ./... -count=1` | PASS；config 1.079s，gateway 11.132s，web 1.831s |
| 两项 timing regression `-race -count=20` | PASS；3.606s |
| `go vet ./...` | PASS |
| `gofmt -l cmd internal` | 无输出 |
| `git diff --check` | PASS |
| 全部 `internal/web/*.js` 的 `node --check` | PASS |
| `app-core.test.js`、迁移测试 | PASS |
| canonical script CSP SHA-256 与最终嵌入文档一致 | PASS |
| 全量 Bash/sh 语法与 ShellCheck | PASS |
| bootstrap、备份正常/恶意归档契约 | PASS |
| 所有 workflow + Compose YAML parse | PASS |
| 全 profile `docker compose ... config --quiet` | PASS |
| systemd 两个 unit 的 `systemd-analyze verify` | PASS |
| Dockerfile digest、禁止 `COPY --chmod`、Action SHA、workflow read-only policy | PASS |
| 构建后二进制 `-check-config config.example.json` | PASS：4 accounts / 3 routes |

### 4.2 集成红队已执行

- Docker 29 legacy builder 实际构建 Main、CLIProxy、Gemini 三张本地镜像。
- Main、CLIProxy、Gemini、Grok 均以非 root + read-only 文件系统完成冷启动 smoke；三个本地产物 `User=10001:10001`。
- CLIProxy 三份维护 patch 在干净 `785b00c` worktree 逐个 `--check/apply`，`diff --check` 与 SHA 均一致。
- Compose 缩减 capability/volume 契约通过；临时容器与卷均已清理。
- 独立数据面 reviewer 对认证 generation、内存租约、transport delayed close、route fingerprint/readiness、下游中性结果和日志恢复做了对抗复核，最终未发现新的 P0/P1。

### 4.3 没有伪装成已验证的部分

- 本机没有 `age`，因此未执行真实 age 加解密 round-trip；fake-age 正常/恶意归档契约已覆盖结构和失败边界。
- 为避免改变在线主机，没有实际执行 root systemd installer、Nginx renderer 或 V2 下载切换；已完成语法、unit、隔离 fixture 和失败恢复测试。
- 本机没有 npm，未本地执行 Playwright 浏览器 E2E；Node、Go embed、inline script、结构契约全部通过，CI 使用 lockfile 执行浏览器工作流。
- 在线 `apk/git/go/npm` 依赖仍不是完全离线、字节级可重建；V2Fly digest 与包仍来自同一发布渠道。
- 工作树中的 `third_party/cliproxyapi` 既有维护改动没有 reset、覆盖或误纳入清理。

## 5. 剩余架构债务与建议顺序

### 优先级 A：下一个发布周期

1. **CORE-08**：实现明确 `closed -> open -> half-open -> closed/open` 状态机，cooldown 后每个 operation/model 仅允许一个 probe；同时让 `/health` 快照不再聚合最坏 bucket。
2. **UI-18**：把当前单一 controller 继续拆成按视图 owner，并引入 keyed renderer，减少活动区域的整块 DOM 替换。
3. **CORE-28**：把 legacy env Key 标为可审计 break-glass，支持 models/RPM/concurrency/expiry，完成迁移后允许禁用。
4. **CORE-23**：将 capability 扩展为 model + operation + feature + effort + upstream model，路由编译和 `/v1/models` 使用同一矩阵。

### 优先级 B：随后两个迭代

1. **CORE-20**：建立 typed domain errors 与中央 protocol mapper，删除调用点手写 status/kind/code。
2. **CORE-26**：把 discovery 移到有 TTL、provenance、generation 的 observed store，只有显式 promotion 才写 desired config。
3. **UI-06/UI-11**：把动态进度样式迁到受控 CSS class，并对高频列表采用 keyed DOM patch。
4. **UI-14**：增加测试子集选择、渠道价格元数据与货币成本预估，而不只是硬上限。
5. request-log 增加持久化失败告警/指标；prompt-test 增加独立 response idle timeout。

### 优先级 C：交付证明与灾备

1. **DEP-17**：产出 SBOM、漏洞报告、provenance 与签名；锁定 apk 包或引入离线制品仓；验证重复构建 digest。
2. **DEP-18**：自动复制到异地不可变存储、retention/轮换、KMS 分离，并定期在隔离 VM 执行真实恢复演练。
3. 设计跨 config、keys、logs、channel runtime 的停写/快照协议，避免多数据源时间点不一致。

## 6. 发布判断

从核心数据面、控制面事务、部署一致性和供应链门禁看，本轮已经消除基线中的全部 P0，并闭合已识别的发布阻断故障；最终非缓存 race 与独立红队均通过。

但这不等于“架构债务归零”。CORE-08 的 half-open 单探针仍是主要历史 P1；前端多版本叠层和任意内联脚本授权已经移除。剩余前端债务集中在 controller 体积、活动区域 keyed patch，以及为自包含样式保留的 `style-src 'unsafe-inline'`，应按上述顺序继续收敛。

本次只完成代码、测试、文档和本地交付验证；**没有执行生产部署、commit 或 push**。
