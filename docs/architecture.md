# Architecture

V0 的目标是以最少的基础设施验证诊断闭环：

```
用户问题 → 固定诊断流程 → 工具证据 → 根因结论
```

## 组件

- **demo-api**：被诊断的 Go HTTP 服务，三个接口 `/health`、`/ready`、`/api/data`。连接 Redis 使用 2s 拨号与读写超时；依赖故障时返回 5xx 并写结构化错误日志（含 `service` / `operation` / `error_type` / `message`）。
- **redis**：demo-api 的唯一外部依赖。
- **agent CLI / tools / model**（Day 2 起）：三个只读工具 `get_container_status`、`query_service_logs`、`check_http_endpoint`，每个工具有明确的输入输出 Schema、超时、错误类型与输出上限，不开放任意 shell。

## 故障注入方式

- Docker：`docker compose stop redis` / `start redis`。
- 无 Docker：`scripts/local-up.sh` 为 redis 与 demo-api 分别维护 PID 文件（`.run/`）；验证脚本按 PID 单独停止 Redis 模拟依赖故障，demo-api 进程保持存活，恢复时重新拉起 Redis。

## 0.X 版本边界

在固定流程和规则校验建立可靠基线前，不上 Kubernetes、RAG、多 Agent、自动修复或 Web 控制台。
