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

- Day 1（当前）：仓库、Go 服务 + Redis + Compose，稳定复现"Redis 停止"故障
- Day 2：三个只读诊断工具（容器状态、服务日志、HTTP 检查）
- Day 3-4：接入 Eino，固定顺序诊断工作流与结构化输出
- Day 5：5 个离线评测 Case
- Day 6-7：运行可观测性、文档与演示

详见 [docs/architecture.md](docs/architecture.md)。
