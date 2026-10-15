# incident-agent-lab

个人实验项目：在本地跑通"制造故障 → 采集证据 → 判断根因 → 自动评测"的最小诊断闭环。

**V0 首个场景**：Go HTTP 服务 `demo-api` 依赖 Redis，Redis 停止后业务接口返回 500，并产生结构化错误日志。V0 只使用 Docker Compose，不引入 Kubernetes、RAG、模型自动修复。

## 仓库结构

```
incident-agent-lab/
├── cmd/
│   ├── demo-api/          # 被诊断的 Go 服务
│   ├── agent/             # Agent CLI
│   └── eval/              # 离线评测套件入口
├── internal/
│   ├── agent/             # Agent 编排
│   ├── tools/             # 只读诊断工具
│   ├── model/             # 状态和结果结构
│   └── rediskv/           # 最小 Redis 客户端
├── deploy/
│   └── compose.yaml       # demo-api + redis
├── scripts/               # 本地（无 Docker）启停与验证脚本
├── scenarios/             # 故障注入和预期结果
├── evals/                 # 评测套件（Case 定义、环境编排、合成靶标）
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

## 离线评测套件（Day 5）

`make eval` 自动完成"准备环境 → 注入故障 → 运行 `agent diagnose` → 断言根因 → 恢复"的完整闭环，并对 5 个 Case 逐一评测，结果落盘为 JSON 报告。评测进程会剥离 `OPENAI_*` / `COZELOOP_*` 环境变量，固定走确定性规则诊断器，保证无 API Key、可重复。

| Case | 故障注入 | 预期 status | 预期 root_cause | 模式 |
|------|----------|-------------|-----------------|------|
| `healthy` | 无故障基线 | `no_incident` | `no_incident` | docker / local |
| `redis_stopped` | 停止 Redis（/ready 503 + /api/data 500） | `diagnosed` | `redis_not_running` | docker / local |
| `app_stopped` | 停止 demo-api 容器（/health 不可达 + 容器 exited） | `diagnosed` | `app_stopped` | 仅 docker |
| `endpoint_timeout` | blackhole 靶标：接受 TCP 连接但永不响应 | `diagnosed` | `app_endpoint_timeout` | docker / local |
| `ambiguous_signals` | 固定返回 404 的靶标，信号不匹配任何规则 | `insufficient_evidence` | `unknown` | docker / local |

```bash
make eval                                    # go run ./cmd/eval，自动选择 Docker 或本地
go run ./cmd/eval --mode local               # 强制本地模式（app_stopped 记为 SKIP）
go run ./cmd/eval --case redis_stopped       # 只跑单个 Case
go run ./cmd/eval --keep                     # 结束后保留环境便于排查
```

评测套件用 Go 实现（`cmd/eval` 入口 + `evals/` 包），与仓库同语言、无 Python 依赖：Case 是 [evals/cases.go](evals/cases.go) 中带类型的结构体，预期根因/状态直接引用 `internal/model` 的常量（如 `model.CauseRedisNotRunning`），不可能与 Agent 的枚举漂移；环境编排（[stack.go](evals/stack.go)）负责构建 `bin/agent`、起停栈与故障注入；合成靶标（[faulttarget.go](evals/faulttarget.go)）用标准库 `net` 实现 blackhole 与固定状态码服务。评测始终执行**真实的 `bin/agent diagnose` 二进制**做端到端验证，而不是直接调用内部函数。

每个 Case 的原始 Agent Report、汇总结果写入 `.run/evals/`（`results-latest.json` 为最新报告，另有时间戳副本）。断言字段为 `diagnosis.status`、`diagnosis.root_cause`、`diagnosis_source=rules`；任一 Case FAIL/ERROR 时退出码非 0，SKIP 不计失败。

docker 模式使用独立 project（`ial-eval-<随机后缀>`）与运行时随机选择的空闲端口（compose 文件动态生成在 `.run/evals/compose.eval.yaml`，Redis 不发布宿主端口），不会与 `make up` 的 18080/6379 冲突；local 模式同样通过 `HTTP_PORT`/`REDIS_PORT` 选用随机端口，与本机其他服务隔离。

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
- Day 4：固定工作流经 compose.ToolsNode 编排执行（工具不可由模型选择），Report 输出逐步执行轨迹 trace
- Day 5（当前）：5 个离线评测 Case，`make eval` 自动准备环境、注入故障并输出 JSON 评测报告
- Day 6-7：运行可观测性（Run ID/工具轨迹审计已有）、文档与演示

详见 [docs/architecture.md](docs/architecture.md)。
