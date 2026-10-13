# incident-agent-lab

个人实验项目：在本地跑通"制造故障 → 采集证据 → 判断根因 → 自动评测"的最小诊断闭环。

**V0 首个场景**：Go HTTP 服务 `demo-api` 依赖 Redis，Redis 停止后业务接口返回 500，并产生结构化错误日志。V0 只使用 Docker Compose，不引入 Kubernetes、RAG、模型自动修复。

## 仓库结构

```
incident-agent-lab/
├── cmd/
│   ├── demo-api/          # 被诊断的 Go 服务
│   └── agent/             # Agent CLI（后续阶段）
├── internal/
│   ├── agent/             # Agent 编排（后续阶段）
│   ├── tools/             # 只读诊断工具（后续阶段）
│   ├── model/             # 状态和结果结构（后续阶段）
│   └── rediskv/           # 最小 Redis 客户端
├── deploy/
│   └── compose.yaml       # demo-api + redis
├── scripts/               # 本地（无 Docker）启停与验证脚本
├── scenarios/             # 故障注入和预期结果
├── evals/                 # 评测代码与数据
├── runbooks/              # 故障处理文档
└── docs/
    └── architecture.md
```

## 接口

| 接口 | 说明 |
|------|------|
| `GET /health` | 进程存活即 200 |
| `GET /ready` | 检查 Redis 可访问性；不可用时 503 |
| `GET /api/data` | 从 Redis 读取数据；依赖不可用时 500 |

Redis 不可用时输出结构化错误日志：

```json
{
  "level": "error",
  "service": "demo-api",
  "operation": "redis_get",
  "error_type": "connection_refused",
  "message": "failed to connect to redis"
}
```

## 快速开始（Docker Compose）

```bash
make up                 # docker compose up -d --build
curl -s localhost:8080/ready
curl -s localhost:8080/api/data

make fault-inject       # docker compose stop redis
curl -i localhost:8080/ready    # 503
curl -i localhost:8080/api/data # 500
make logs               # 查看结构化错误日志

make fault-recover      # docker compose start redis
make down
```

## 诊断工具（Day 2）

三个只读工具通过 agent CLI 调用，不开放任意 shell。每次调用都会向 `.run/tool-calls.jsonl` 追加一条审计记录（run_id、入参、耗时、错误类型、结果）。

```bash
make build

# 接口检查：状态码 / 延迟 / 响应摘要（5xx 是正常结果而非工具错误）
./bin/agent check-http -url http://localhost:8080/ready
./bin/agent check-http -url http://localhost:8080/api/data -timeout-ms 3000

# 容器状态：status / running / exit_code / 时间信息
./bin/agent container-status -container ial-redis

# 服务日志：按 error_type 聚合数量与一条样例，不回吐原始日志
./bin/agent service-logs -service ial-demo-api -tail 200
```

工具错误统一归类为：`timeout` / `connection_failed` / `container_not_found` / `docker_unavailable` / `invalid_input` / `internal_error`。所有工具都有超时（HTTP 默认 3s 上限 10s；docker 命令 5-8s）、输入校验与输出大小上限（body 摘要 200 字符、日志最多 20 个错误组）。

## Agent 诊断工作流（Day 3-4，Eino）

三个工具通过 [Eino](https://github.com/cloudwego/eino) `utils.InferTool` 注册为带 JSON Schema 的 InvokableTool。证据采集不是模型自由选工具，而是通过 Eino `compose.ToolsNode` 执行一份固定有序的步骤表（Day 4）：

```
check_http(/health → /ready → /api/data) → query_service_logs → get_container_status(redis → app)
                                                              ↓
                                            CollectedEvidence → 诊断 → 规则校验 → Report
```

- **固定编排**：[toolnode.go](internal/agent/toolnode.go) 的 `FixedWorkflowToolNode` 逐步构造 Assistant ToolCall 消息交给真实 ToolsNode 执行，工具顺序在代码中确定、模型不能改；启动时校验每个步骤引用的工具均已注册。工具失败以 `{ok:false}` 信封返回而非框架错误，因此 6 个步骤始终全部执行，失败降级为观测。
- **执行轨迹**：Report 新增 `trace`，逐步记录 step/tool/ok/error_type/latency_ms，与审计 JSONL 一起支撑 Day 6 的运行可观测性。

诊断器分两层：
- **规则诊断器（默认，无需模型）**：基于采集到的 HTTP 状态码、日志错误类型和容器状态做确定性判断，输出根因枚举，保证无 API Key 也能跑通闭环，也是评测基线。
- **模型诊断器（可选）**：设置 `OPENAI_API_KEY`（兼容 `OPENAI_BASE_URL` / `MODEL_NAME`）后启用 Eino `openai.NewChatModel`，模型只产出根因、置信度和证据引用，其结论必须通过与规则诊断器同源的证据校验，无证据支持时回退规则结论——不开放自动修复。

根因枚举：`redis_not_running` / `app_stopped` / `app_endpoint_timeout` / `no_incident` / `unknown`；无充分证据时返回 `insufficient_evidence`。

```bash
make build

# 无 Docker daemon 的本地模式也能诊断（容器/日志工具失败成为观测，
# 仅凭 /ready 503 + /api/data 500 信号即可判为 redis_not_running）
./bin/agent diagnose
./bin/agent diagnose --base-url http://localhost:8080 \
  --app-container ial-demo-api --redis-container ial-redis

# 启用模型
OPENAI_API_KEY=sk-xxx OPENAI_BASE_URL=https://api.openai.com/v1 \
  MODEL_NAME=gpt-4o-mini ./bin/agent diagnose
```

`diagnose` 始终输出 Report JSON（`diagnosis_source` 标记 `rules`/`model`），单工具失败不中断流程；每次工具调用都写入审计 JSONL。

### Coze Loop 追踪（可选）

设置 Coze Loop（[cozeloop-go](https://github.com/coze-dev/cozeloop-go)）凭证后，`diagnose` 自动上报一条 trace，无需改命令：

```bash
export COZELOOP_API_TOKEN=pat_xxx          # PAT 或 JWT
export COZELOOP_WORKSPACE_ID=<workspace id>
# export COZELOOP_API_BASE_URL=https://api.coze.cn   # 私有化/海外时覆盖
./bin/agent diagnose
```

trace 结构：根 span `incident.diagnose`（type `workflow`）→ 6 个固定工具子 span（type `tool`，含入参、信封输出、失败状态码）→ 启用模型时一个 `diagnose.llm` span（type `model`，含 model_name、prompt/completion tokens、校验回退标记）。未设置两个环境变量时追踪整体关闭（无网络请求、无凭证报错），诊断行为与审计 JSONL 不受影响。

## 无 Docker 环境的本地方式

需要本机已安装 Go 和 redis-server：

```bash
make local-up           # 后台启动 redis-server 与 bin/demo-api（PID/日志在 .run/）
make verify             # 端到端验证：正常 → 停 Redis → 恢复
make local-down
```

## 测试

```bash
make test
```

## 路线图

- Day 1：Go 服务 + Redis + Compose，稳定复现"Redis 停止"故障
- Day 2：三个只读诊断工具（容器状态、日志聚合、HTTP 检查）+ CLI 与调用审计
- Day 3：接入 Eino——工具注册为 InvokableTool，ChatModel（OpenAI 兼容）+ 证据规则校验，无 Key 时确定性规则诊断
- Day 4（当前）：固定工作流经 compose.ToolsNode 编排执行（工具不可由模型选择），Report 输出逐步执行轨迹 trace
- Day 5：5 个离线评测 Case
- Day 6-7：运行可观测性（Run ID/工具轨迹审计已有）、文档与演示

详见 [docs/architecture.md](docs/architecture.md)。
