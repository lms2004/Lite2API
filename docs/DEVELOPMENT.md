# Lite2API 开发与验证

## Go 工具链

`go.mod` 的最低语言版本是 Go 1.23。仓库的固定构建工具链为 Go 1.26.5：

- `Dockerfile` 与 `make test-docker` 使用 `golang:1.26.5-alpine`；
- 本机命令统一设置 `GOTOOLCHAIN=local`，禁止 `go` 在测试过程中临时下载另一套工具链；
- 当前服务器的官方二进制安装在 `/usr/local/go`，`/usr/local/bin/go` 与 `gofmt` 指向该目录。

先确认工具链，再执行测试：

```bash
make toolchain
make test
make test-web
make test-deploy
```

有 Docker 运行时时，可在与宿主机隔离的固定镜像中验证：

```bash
make test-docker
```

如果本机没有合适版本，从 [Go 官方下载页](https://go.dev/dl/) 获取 Linux amd64 归档，核对页面提供的 SHA-256 后解压到 `/usr/local/go`。不要依赖旧版 Go 的自动 toolchain 下载；受限网络中它会在测试开始前失败。

## 管理台检查

`make verify` 汇总 Go 竞态、前端业务测试与脚本语法、部署回归及 diff 检查。Node.js 只用于开发验证，运行网关不需要 Node.js。管理台脚本在 `internal/web/app-*.js`，由 `make test-web` 直接检查，无需从 HTML 提取脚本。

业务变更以 [能力图](BUSINESS_CAPABILITIES.md)、[八个业务用例](BUSINESS_USE_CASES.md) 和 [第二轮验收记录](BUSINESS_ALIGNMENT_ROUND2_2026-10-28.md) 为依据；第一轮记录保留其历史范围。协议或鉴权变更需要真实 HTTP 夹具证明调用、失败分支和产物，不能只检查配置结构。可用 `go test -race ./internal/gateway -run 'TestCompletion|TestSSECompletion|TestCompletionReview|TestReadiness|TestRequestEvidence|TestAttemptEvidence|TestRequestRequirements|TestCapability|TestManaged|TestAccountProbe|TestPromptTest'` 缩小复验范围。

响应式改动的验收覆盖 320、768、1024 和 1440 CSS 像素，检查无横向滚动、移动导航、焦点恢复、图表文本摘要与 reduced-motion。遵循 `AGENTS.md` 的单浏览器、硬超时与生产只读约定；纯服务端改动不必重跑浏览器。

## 生命周期与真实验收

`make verify` 还运行生命周期回归：需要真实 `age`/`age-keygen` 与隔离 `chroot` 的权限（通常在 root 测试容器或专用验证环境中执行）。`make test-lifecycle` 覆盖真实加密还原、真 Go 网关调用/Key 规则与安装器故障回退；测试只创建私有目录、临时 Git 仓库及受控进程。systemctl 夹具只提供隔离进程管理，业务 HTTP 不伪造通过，不宣称真实 systemd 加固验证。

显式设置 `LITE2API_TEST_DOCKER=1` 后，再以唯一测试镜像/卷/容器验证 Compose 材料运行和真实快照暂停/恢复。未设置或 Docker 不可用时应保留 skip，不能算 Docker 验收通过。取消测试的受控 age 输出只验证清理，真实加解密由独立用例完成。

真实外部来源检查使用带完整隔离参数的命令，例如：

```bash
python3 tools/verify-business-runtime.py \
  --binary ./lite2api \
  --source-config /secure/source-config.json \
  --source-env /secure/source.env \
  --route Shadow \
  --output /secure/acceptance-summary.json \
  --source-verification --tool-clients --claude --codex \
  --only responses_stream messages_stream
```

`--only` 只接受 `chat_json`、`chat_stream`、`responses_json`、`responses_stream`、`messages_json`、`messages_stream`；省略它会运行全部六种隔离 HTTP 组合。工具客户端参数要求真实客户端读取随机测试文件并完成工具往返。脚本创建私有网关，不发布生产，保存精确来源的能力事实与摘要；客户端进程只获得自己的临时网关 Key，不能继承上游/管理凭据。首次失败及修订后复验分开保存。
