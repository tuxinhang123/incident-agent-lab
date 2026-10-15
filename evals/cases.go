package evals

import (
	"context"
	"net/http"
	"time"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

// Case is one deterministic offline evaluation. Hooks run against the
// shared Harness; expected outcomes reference the model package constants
// so a typo cannot drift from the agent's real taxonomy.
type Case struct {
	Index    int
	ID       string
	Title    string
	Question string
	Modes    []Mode

	// TimeoutMS overrides the agent's per-request timeout (0 = default).
	TimeoutMS int

	// Inject realizes the fault; WaitFault blocks until it is observable;
	// Recover restores the stack baseline before the next case.
	Inject    func(ctx context.Context, h *Harness) error
	WaitFault func(ctx context.Context, h *Harness) error
	Recover   func(ctx context.Context, h *Harness) error

	ExpectedStatus    string
	ExpectedRootCause string
}

func noopHook(context.Context, *Harness) error { return nil }

// Cases returns the five Day 5 cases in execution order.
func Cases() []Case {
	return []Case{
		{
			Index:             1,
			ID:                "healthy",
			Title:             "基线健康：demo-api 与 Redis 均正常",
			Question:          "巡检 demo-api，当前是否存在故障？",
			Modes:             []Mode{ModeDocker, ModeLocal},
			Inject:            noopHook,
			WaitFault:         noopHook,
			Recover:           noopHook,
			ExpectedStatus:    model.StatusHealthy,
			ExpectedRootCause: model.CauseNone,
		},
		{
			Index:    2,
			ID:       "redis_stopped",
			Title:    "Redis 停止导致 /ready 503、/api/data 500",
			Question: "demo-api 接口持续返回 500，请定位原因",
			Modes:    []Mode{ModeDocker, ModeLocal},
			Inject: func(ctx context.Context, h *Harness) error {
				return h.StopRedis(ctx)
			},
			WaitFault: func(ctx context.Context, h *Harness) error {
				return h.waitForCode(ctx, h.BaseURL()+"/ready", http.StatusServiceUnavailable, 20*time.Second)
			},
			Recover: func(ctx context.Context, h *Harness) error {
				if err := h.StartRedis(ctx); err != nil {
					return err
				}
				return h.waitForCode(ctx, h.BaseURL()+"/ready", http.StatusOK, 30*time.Second)
			},
			ExpectedStatus:    model.StatusDiagnosed,
			ExpectedRootCause: model.CauseRedisNotRunning,
		},
		{
			Index:    3,
			ID:       "app_stopped",
			Title:    "demo-api 容器停止，健康检查不可达",
			Question: "demo-api 的 /health 完全无法访问，请定位原因",
			Modes:    []Mode{ModeDocker}, // needs real container-status evidence
			Inject: func(ctx context.Context, h *Harness) error {
				return h.StopApp(ctx)
			},
			WaitFault: func(ctx context.Context, h *Harness) error {
				return h.waitForDown(ctx, h.BaseURL()+"/health", 20*time.Second)
			},
			Recover: func(ctx context.Context, h *Harness) error {
				if err := h.StartApp(ctx); err != nil {
					return err
				}
				return h.waitForCode(ctx, h.BaseURL()+"/health", http.StatusOK, 30*time.Second)
			},
			ExpectedStatus:    model.StatusDiagnosed,
			ExpectedRootCause: model.CauseAppStopped,
		},
		{
			Index:     4,
			ID:        "endpoint_timeout",
			Title:     "目标端点接受连接但永不响应（请求超时）",
			Question:  "demo-api 所有接口请求都卡住直到超时，请定位原因",
			Modes:     []Mode{ModeDocker, ModeLocal},
			TimeoutMS: 1000,
			Inject: func(ctx context.Context, h *Harness) error {
				return h.UseBlackhole()
			},
			WaitFault: noopHook,
			Recover: func(ctx context.Context, h *Harness) error {
				h.ClearFault()
				return nil
			},
			ExpectedStatus:    model.StatusDiagnosed,
			ExpectedRootCause: model.CauseAppTimeout,
		},
		{
			Index:    5,
			ID:       "ambiguous_signals",
			Title:    "证据信号相互不匹配，无法得出根因",
			Question: "巡检目标接口全部返回 404，请判断发生了什么",
			Modes:    []Mode{ModeDocker, ModeLocal},
			Inject: func(ctx context.Context, h *Harness) error {
				return h.UseStatusStub(http.StatusNotFound)
			},
			WaitFault: noopHook,
			Recover: func(ctx context.Context, h *Harness) error {
				h.ClearFault()
				return nil
			},
			ExpectedStatus:    model.StatusInsufficientEvidence,
			ExpectedRootCause: model.CauseUnknown,
		},
	}
}

// Supports reports whether the case can run in the given mode.
func (c Case) Supports(mode Mode) bool {
	for _, m := range c.Modes {
		if m == mode {
			return true
		}
	}
	return false
}
