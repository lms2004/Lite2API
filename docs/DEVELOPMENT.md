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
```

有 Docker 运行时时，可在与宿主机隔离的固定镜像中验证：

```bash
make test-docker
```

如果本机没有合适版本，从 [Go 官方下载页](https://go.dev/dl/) 获取 Linux amd64 归档，核对页面提供的 SHA-256 后解压到 `/usr/local/go`。不要依赖旧版 Go 的自动 toolchain 下载；受限网络中它会在测试开始前失败。

## 管理台检查

```bash
make test
node --check /tmp/lite2api-admin.js
git diff --check
```

JavaScript 检查时从 `internal/web/index.html` 提取内联脚本到临时文件；临时文件不提交。响应式验收至少覆盖 320、768、1024 和 1440 CSS 像素，并检查无横向页面滚动、核心四项移动导航、弹窗焦点恢复、图表文本摘要和 reduced-motion。
