# Operations

## 上线检查

1. `server.listen` 保持 `127.0.0.1:45679`。
2. 网关 Key 和管理员 Token 使用两个不同的 32 字节以上随机值。
3. 上游密钥放入 `.env`，不要写进 JSON 或 Git。
4. `data/config.json`、`.env` 权限设为 `0600`。
5. 只通过 Nginx HTTPS 暴露模型 API；管理页通过 SSH Tunnel/VPN 访问。
6. 先检查 `/health`、`/v1/models`，再执行一条流式真实请求。

## 运行检查

```bash
docker compose ps
docker compose logs --tail=100 lite2api
curl -fsS http://127.0.0.1:45679/health
docker stats --no-stream lite2api
```

## 更新与回滚

配置更新由程序先验证，再以临时文件 + `fsync` + 原子 rename 保存。建议更新镜像前保留当前镜像 ID：

```bash
docker image inspect lite2api:local --format '{{.Id}}'
docker compose build
docker compose up -d
curl -fsS http://127.0.0.1:45679/health
```

镜像升级失败时，将 Compose 的 `image` 改回旧标签后重新 `up -d`。配置回滚只需恢复 `data/config.json` 并发送 HUP。

## 备份

必须备份：

- Docker 命名卷 `lite2api-data` 中的 `/app/data/config.json`
- `.env`

程序没有数据库。运行统计在内存中，重启丢失，不属于需要恢复的业务状态。

## 故障判断

- `503 no healthy account...`：路由没有健康账号，或账号并发槽全部占满并超过等待时间。
- `429 gateway concurrency limit reached`：入口总并发保护生效。
- 账号显示 `circuit_open_until`：连续失败、鉴权错误或上游限流触发熔断。
- `upstream stream idle`：响应头成功，但上游在配置时间内没有继续产生数据。
- 配置重载失败：旧运行配置继续提供服务；修复 JSON 后再次 reload。

## 容量建议

个人两核机器默认值已经有较大余量。账号 `concurrency` 应按上游真实允许值设置；总入口 `max_inflight_requests` 用于保护本机，而不是扩大上游配额。大请求较多时优先降低 `max_body_bytes` 和 `max_inflight_requests`。
