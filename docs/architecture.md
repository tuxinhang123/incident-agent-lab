# Architecture

V0 的目标是以最少的基础设施验证诊断闭环：

```
用户问题 → 固定诊断流程 → 工具证据 → 根因结论
```

## 组件

- **demo-api**：被诊断的 Go HTTP 服务，三个接口 `/health`、`/ready`、`/api/data`。连接 Redis 使用 2s 拨号与读写超时；依赖故障时返回 5xx 并写结构化错误日志（含 `service` / `operation` / `error_type` / `message`）。
- **redis**：demo-api 的唯一外部依赖。
- **只读工具（Day 2）**：`check_http_endpoint`、`query_service_logs`、`get_container_status`，每个工具有输入输出 Schema、超时、错误类型与输出上限，不开放任意 shell。
- **Eino 工作流（Day 3-4）**：三个工具经 `utils.InferTool` 注册为 Eino InvokableTool（`internal/agent/einotools.go`）。固定顺序由 `FixedWorkflowToolNode`（`internal/agent/toolnode.go`）承载——它持有真实的 `compose.ToolsNode`，按 `buildFixedSteps`（`collector.go`）中的步骤表逐步发送单 ToolCall 的 Assistant 消息执行：HTTP 三接口 → 日志聚合 → Redis/应用容器状态，共 6 步。模型不能选择或增删工具；构造期校验步骤引用的工具名均已注册。
  - 工具失败不中断：工具把传输/分类错误封装为 `{ok:false,error_type}` 信封返回 Go nil，ToolsNode 不会因单步失败中止，失败结果成为 `CollectedEvidence` 中的失败观测。
  - `Report.trace` 逐步记录 step/tool/ok/error_type/latency_ms；每次工具调用另写脱敏审计 JSONL。
  - 诊断器分两层：规则诊断器 `DiagnoseRules` 为确定性基线与回退；模型诊断器 `DiagnoseWithModel`（eino-ext openai，OpenAI 兼容）的输出经 `Validate` 与证据交叉校验（根因必须与规则基线一致、证据逐项可重建、status/confidence 合法），不通过即回退规则结论。

## 故障注入方式

- Docker：`docker compose stop redis` / `start redis`。
- 无 Docker：`scripts/local-up.sh` 为 redis 与 demo-api 分别维护 PID 文件（`.run/`）；验证脚本按 PID 单独停止 Redis 模拟依赖故障，demo-api 进程保持存活，恢复时重新拉起 Redis。

## 0.X 版本边界

在固定流程和规则校验建立可靠基线前，不上 Kubernetes、RAG、多 Agent、自动修复或 Web 控制台。
