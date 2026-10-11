# Architecture

V0 的目标是以最少的基础设施验证诊断闭环：

```
用户问题 → 固定诊断流程 → 工具证据 → 根因结论
```

## 组件

- **demo-api**：被诊断的 Go HTTP 服务，三个接口 `/health`、`/ready`、`/api/data`。连接 Redis 使用 2s 拨号与读写超时；依赖故障时返回 5xx 并写结构化错误日志（含 `service` / `operation` / `error_type` / `message`）。
- **redis**：demo-api 的唯一外部依赖。
- **只读工具（Day 2）**：`check_http_endpoint`、`query_service_logs`、`get_container_status`，每个工具有输入输出 Schema、超时、错误类型与输出上限，不开放任意 shell。
- **Eino 工作流（Day 3）**：三个工具经 `utils.InferTool` 注册为 Eino InvokableTool（`internal/agent/einotools.go`）。固定顺序收集证据（HTTP → 日志 → 容器），结果汇聚为 `CollectedEvidence`；诊断器分两层：
  - 规则诊断器 `DiagnoseRules`：确定性映射 HTTP 状态码、日志 error_type、容器状态到根因枚举，无 Key 可运行，是评测基线与回退路径。
  - 模型诊断器 `DiagnoseWithModel`：OpenAI 兼容 ChatModel（eino-ext openai），只解释证据；输出经 `Validate` 与证据交叉校验，无证据支持的根因一律拒绝并回退规则结论。
  - 工具失败不中断流程：失败结果作为观测进入 `CollectedEvidence`，证据不足时输出 `insufficient_evidence` 而非猜测。

## 故障注入方式

- Docker：`docker compose stop redis` / `start redis`。
- 无 Docker：`scripts/local-up.sh` 为 redis 与 demo-api 分别维护 PID 文件（`.run/`）；验证脚本按 PID 单独停止 Redis 模拟依赖故障，demo-api 进程保持存活，恢复时重新拉起 Redis。

## 0.X 版本边界

在固定流程和规则校验建立可靠基线前，不上 Kubernetes、RAG、多 Agent、自动修复或 Web 控制台。
