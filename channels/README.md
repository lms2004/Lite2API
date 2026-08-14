# 渠道适配层

Lite2API 核心不复制第三方项目的账号登录、Cookie 刷新或逆向协议代码。每个渠道作为独立进程暴露 OpenAI 兼容接口，核心只负责统一入口、密钥隔离、模型映射、并发、熔断、换号与统计。

当前固定源码：

- `third_party/grok2api`：Grok Build/Web/Console，多账号由它自己的管理页维护。
- `third_party/gemini-web2api`：Gemini Web；匿名 Flash 可直接使用，Pro 需要在私有运行配置中添加 Cookie。
- AtomCode2Api：现有独立容器，Lite2API 通过 `127.0.0.1:45678/v1` 接入。

初始化并按需启动：

```bash
./deploy/bootstrap-channels.sh
docker compose -f docker-compose.yml -f compose.channels.yml --profile gemini up -d --build
docker compose -f docker-compose.yml -f compose.channels.yml --profile grok up -d
```

端口只监听本机：Grok `45680`、Gemini `45681`。运行密钥和 Cookie 放在 `channels/runtime/`，该目录不会进入 Git。

Grok 首次启动后访问 `http://127.0.0.1:45680`，使用 `.env` 中的 `GROK2API_ADMIN_PASSWORD` 登录、导入账号并创建 Client Key，再把 Client Key 写入 `.env` 的 `GROK2API_KEY`，重建 Lite2API 容器并在管理页启用 `grok-local`。

Gemini 默认使用临时会话、关闭请求日志、30 秒快速失败，并由随机内部 Key 保护。精简镜像刻意不安装可选 `httpx`：流式请求会先完整获取结果再输出标准 SSE，以避免 Gemini Web 长连接不结束导致并发槽悬挂。需要 Pro 或修复地区可用性时，只编辑 `channels/runtime/gemini-web2api/config.json` 中的 Cookie/代理配置，不要把凭据写进模板或 Git。

第三方渠道独立升级：

```bash
git submodule update --remote third_party/grok2api
git submodule update --remote third_party/gemini-web2api
```

升级后先在回环端口完成健康检查和真实请求，再切换 Lite2API 路由。不要自动跟随 `main` 部署。
