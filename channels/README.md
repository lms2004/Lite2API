# 渠道适配层

Lite2API 核心不复制第三方项目的账号登录、Cookie 刷新或逆向协议代码。每个渠道作为独立进程暴露 OpenAI 兼容接口，核心只负责统一入口、密钥隔离、模型映射、并发、熔断、换号与统计。

当前固定源码：

- `third_party/cliproxyapi`：Gemini CLI、Claude、OpenAI/Codex、Antigravity 的 OAuth/setup-token 多账号池；固定为 `v6.10.9`（`785b00c3127eea6aa207f1207ead8a2aa93690a3`）。
- `third_party/grok2api`：Grok Build/Web/Console，多账号由它自己的管理页维护。
- `third_party/gemini-web2api`：Gemini Web；匿名 Flash 可直接使用，Pro 需要在私有运行配置中添加 Cookie。
- AtomCode2Api：现有独立容器，Lite2API 通过 `127.0.0.1:45678/v1` 接入。

初始化并按需启动：

```bash
./deploy/bootstrap-channels.sh
docker compose -f docker-compose.yml -f compose.channels.yml --profile gemini up -d --build
docker compose -f docker-compose.yml -f compose.channels.yml --profile grok up -d
docker compose -f docker-compose.yml -f compose.channels.yml --profile oauth up -d --build
```

端口只监听本机：Grok `45680`、Gemini `45681`、CLIProxyAPI `45682`。运行密钥、OAuth 文件和 Cookie 放在 `channels/runtime/`，该目录不会进入 Git。

旧 Sub2API 账号导出可以按凭据类型一次拆分到三个安全边界：官方 API Key 进入 Lite2API，Grok OAuth 进入 Grok2API，其余受支持的 OAuth/setup-token 进入 CLIProxyAPI：

```bash
node deploy/migrate-sub2api-auths.mjs \
  --input /path/to/sub2api-export.json \
  --cliproxy-auth-dir channels/runtime/cliproxyapi/auths \
  --grok-output channels/runtime/cliproxyapi/grok-import.json \
  --lite-output channels/runtime/cliproxyapi/lite-import.json
```

脚本只输出分类数量，生成目录为 `0700`、凭据文件为 `0600`。它不会把 OAuth/Cookie 当作 API Key。生成文件仍需分别通过适配器/Lite2API 管理接口导入并验证，禁止提交 Git。

Grok 首次启动后访问 `http://127.0.0.1:45680`，使用 `.env` 中的 `GROK2API_ADMIN_PASSWORD` 登录、导入账号并创建 Client Key，再把 Client Key 写入 `.env` 的 `GROK2API_KEY`，重建 Lite2API 容器并在管理页启用 `grok-local`。

Gemini 默认使用临时会话、关闭请求日志、30 秒快速失败，并由随机内部 Key 保护。精简镜像刻意不安装可选 `httpx`：流式请求会先完整获取结果再输出标准 SSE，以避免 Gemini Web 长连接不结束导致并发槽悬挂。需要 Pro 或修复地区可用性时，只编辑 `channels/runtime/gemini-web2api/config.json` 中的 Cookie/代理配置，不要把凭据写进模板或 Git。

第三方渠道独立升级：

```bash
git submodule update --remote third_party/grok2api
git submodule update --remote third_party/gemini-web2api
```

CLIProxyAPI 生产版本必须显式修改子模块 revision 和 Compose 的 `VERSION`/`COMMIT`，不使用 `--remote` 自动升级。所有适配器升级后先在回环端口完成健康检查和真实请求，再切换 Lite2API 路由。不要自动跟随 `main` 部署。
