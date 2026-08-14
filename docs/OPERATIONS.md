# Operations

## 上线检查

1. `server.listen` 保持 `127.0.0.1:45679`。
2. 网关 Key 和管理员 Token 使用两个不同的 32 字节以上随机值。
3. 上游密钥放入 `.env`，不要写进 JSON 或 Git。
4. `data/config.json`、`.env` 权限设为 `0600`。
7. `LITE2API_ADMIN_ALLOWED_CIDRS` 只包含 loopback 与实际 VPN 出口；`LITE2API_TRUSTED_PROXY_CIDRS` 只包含 Nginx 的直接来源。
8. Nginx TLS server 中引入 `deploy/nginx-subpath.conf`，并在 reload 前执行 `nginx -t`。
9. 公网验证 `/lite/v1/models` 无 Key 返回 401；非 VPN 来源访问 `/lite-admin/` 返回 403。
10. 检查 `ss -lntup` 与 `docker ps`，Lite2API 和所有渠道适配器只能监听 loopback。
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
- 同一命名卷中的 `/app/data/client_keys.json`（仅包含摘要，但丢失后所有托管 Key 都会失效）
- `.env`

程序没有数据库。运行统计在内存中，重启丢失，不属于需要恢复的业务状态。

## 故障判断

- `503 no healthy account...`：路由没有健康账号，或账号并发槽全部占满并超过等待时间。
- `429 gateway concurrency limit reached`：入口总并发保护生效。
- 账号显示 `circuit_open_until`：连续失败、鉴权错误或上游限流触发熔断。
- `upstream stream idle`：响应头成功，但上游在配置时间内没有继续产生数据。
- `401 invalid_api_key`：客户端 Key 无效、已过期、已禁用或已撤销。
- `403 model_not_allowed`：该 Key 的模型白名单不包含请求模型。
- `429 rate_limit_exceeded`：Key 的 RPM 已达到本分钟上限。
- `429 concurrency_limit_exceeded`：Key 的并发槽已满；流式请求会一直持有槽位。
- 配置重载失败：旧运行配置继续提供服务；修复 JSON 后再次 reload。

## 容量建议

当前 2 核、2 GiB 服务器使用 256 MiB 容器内存、1.5 CPU、32 个入口并发、8 MiB 请求体、64 个单上游连接和 192 MiB Go 软内存限制。账号 `concurrency` 应按上游真实允许值设置；总入口 `max_inflight_requests` 用于保护本机，而不是扩大上游配额。

单机 Key 鉴权和限流应保持内存实现。只有扩展为多个 Lite2API 实例时，才启用 Redis 状态实现；Redis 不负责保存 Key 明文。
