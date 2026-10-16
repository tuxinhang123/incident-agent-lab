package agent

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
	"github.com/tuxinhang123/incident-agent-lab/internal/tools"
)

// Tool names shared by the Eino registry, the workflow and evals.
const (
	ToolCheckHTTP       = "check_http_endpoint"
	ToolServiceLogs     = "query_service_logs"
	ToolContainerGet    = "get_container_status"
	ToolCollectEvidence = "collect_service_evidence"
)

// Tool invocation envelopes: model-facing tools always return a structured
// envelope so transport-level failures are visible to the model instead of
// aborting the chain as opaque Go errors.
type toolEnvelope struct {
	OK        bool   `json:"ok"`
	Result    any    `json:"result,omitempty"`
	ErrorType string `json:"error_type,omitempty"`
	ErrorMsg  string `json:"message,omitempty"`
}

// ToolGateway is the single cross-cutting layer for diagnostic tool calls.
// It wraps every tool invocation with audit logging and the unified
// ok/error envelope. A nil gateway is valid and makes the wrapped tools
// behave as plain envelope-only wrappers — that is the form used by the
// fixed workflow, which manages its own audit entries. The non-nil form is
// used by the agentic path, where model-driven tool calls would otherwise
// not be recorded in the audit trail.
//
// Tracing is deliberately NOT handled here: the Eino ADK CozeLoop callback
// already emits a tool span for every model-driven call, and
// FixedWorkflowToolNode emits per-step spans for composite tools. Adding a
// gateway span would duplicate those in the same trace backend.
type ToolGateway struct {
	audit AuditSink
	runID string
}

// NewToolGateway builds a gateway for the agent path. It returns nil when
// no audit sink is configured, letting callers pass the result straight
// through without a nil-check branch at every construction site.
func NewToolGateway(audit AuditSink, runID string) *ToolGateway {
	if audit == nil {
		return nil
	}
	return &ToolGateway{audit: audit, runID: runID}
}

// invoke runs handler and wraps its (result, ToolError) pair in an envelope.
// When the gateway is non-nil it also records an audit entry. The handler
// receives the request context unchanged so any framework-managed span
// (CozeLoop tool span) remains the parent of nested spans. The method is
// safe to call on a nil receiver: a nil gateway returns the envelope
// without side effects.
func (g *ToolGateway) invoke(ctx context.Context, name string, args map[string]any,
	handler func(ctx context.Context) (any, *model.ToolError)) *toolEnvelope {

	started := nowFn()
	result, tErr := handler(ctx)
	env := wrapResult(result, tErr)

	if g != nil && g.audit != nil {
		var auditResult any
		if env.OK {
			auditResult = result
		}
		_ = g.audit.Record(g.runID, name, args, started, auditResult, tErr)
	}
	return env
}

// wrapResult converts a typed tool result / ToolError pair into the
// model-facing envelope. Non-2xx HTTP statuses and other "result with a
// signal" cases carry tErr == nil and become ok envelopes; transport and
// classification failures become ok=false envelopes carrying the error
// type and message so the ToolsNode never aborts the sequence.
func wrapResult(result any, tErr *model.ToolError) *toolEnvelope {
	if tErr != nil {
		return &toolEnvelope{OK: false, ErrorType: tErr.Type, ErrorMsg: tErr.Message}
	}
	return &toolEnvelope{OK: true, Result: result}
}

// argsMap round-trips a typed tool input struct through JSON so it can be
// recorded in the audit trail and tracing tags. Failure returns nil, which
// the audit layer treats as "no args".
func argsMap(in any) map[string]any {
	b, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}

type httpToolInput struct {
	URL       string `json:"url" jsonschema:"description=Full HTTP(S) URL to GET"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"description=Request timeout in milliseconds, max 10000"`
}

type containerToolInput struct {
	Container string `json:"container" jsonschema:"description=Docker container name or ID"`
}

type logsToolInput struct {
	Service   string `json:"service" jsonschema:"description=Docker container name or service whose logs are queried"`
	TailLines int    `json:"tail_lines,omitempty" jsonschema:"description=Number of recent log lines to scan, max 5000"`
}

// NewHTTPCheckTool registers check_http_endpoint as an Eino invokable tool.
// When gw is non-nil the call is audited and traced; otherwise it is a
// plain envelope wrapper (used by the fixed workflow).
func NewHTTPCheckTool(gw *ToolGateway) (tool.InvokableTool, error) {
	return utils.InferTool(
		ToolCheckHTTP,
		"Performs a GET and returns HTTP status code, latency_ms and a truncated body_summary. Non-2xx responses are reported in the result, not as errors.",
		func(ctx context.Context, in httpToolInput) (*toolEnvelope, error) {
			return gw.invoke(ctx, ToolCheckHTTP, argsMap(in), func(_ context.Context) (any, *model.ToolError) {
				return tools.CheckHTTPEndpoint(tools.HTTPEndpointInput{URL: in.URL, TimeoutMS: in.TimeoutMS})
			}), nil
		},
	)
}

// NewContainerStatusTool registers get_container_status as an Eino tool.
func NewContainerStatusTool(gw *ToolGateway) (tool.InvokableTool, error) {
	return utils.InferTool(
		ToolContainerGet,
		"Returns a Docker container's status, running flag, exit code and timing information from docker inspect.",
		func(ctx context.Context, in containerToolInput) (*toolEnvelope, error) {
			return gw.invoke(ctx, ToolContainerGet, argsMap(in), func(_ context.Context) (any, *model.ToolError) {
				return tools.GetContainerStatus(tools.ContainerStatusInput{Container: in.Container})
			}), nil
		},
	)
}

// NewServiceLogsTool registers query_service_logs as an Eino tool.
func NewServiceLogsTool(gw *ToolGateway) (tool.InvokableTool, error) {
	return utils.InferTool(
		ToolServiceLogs,
		"Reads recent container logs and aggregates structured error entries by error_type with counts and one sample each; raw logs are never returned.",
		func(ctx context.Context, in logsToolInput) (*toolEnvelope, error) {
			return gw.invoke(ctx, ToolServiceLogs, argsMap(in), func(_ context.Context) (any, *model.ToolError) {
				return tools.QueryServiceLogs(tools.ServiceLogsInput{Service: in.Service, Tail: in.TailLines})
			}), nil
		},
	)
}

// RegisteredTools returns all V0 read-only tools in fixed workflow order.
// gw may be nil to build plain envelope-only wrappers (fixed workflow); a
// non-nil gateway wires audit and tracing into every atomic call (agent
// path).
func RegisteredTools(gw *ToolGateway) ([]tool.InvokableTool, error) {
	ctors := []func(*ToolGateway) (tool.InvokableTool, error){
		NewHTTPCheckTool,
		NewServiceLogsTool,
		NewContainerStatusTool,
	}
	out := make([]tool.InvokableTool, 0, len(ctors))
	for _, ctor := range ctors {
		t, err := ctor(gw)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// collectEvidenceInput is empty: the target is baked into the tool at
// construction time so the model cannot misconfigure the baseline probes.
type collectEvidenceInput struct{}

// NewCollectEvidenceTool builds the collect_service_evidence tool. It is a
// deterministic, coarse-grained tool that runs the same fixed evidence
// collection the rule-based path uses (HTTP probes -> app logs -> container
// status) and returns the full CollectedEvidence bundle. The model calls it
// once for the baseline; the atomic tools remain available for targeted
// follow-up.
//
// Like the atomic tools it is routed through the ToolGateway so the composite
// call itself gets an audit entry; the six inner steps executed by
// CollectEvidence produce their own audit entries. Tool spans come from the
// Eino ADK CozeLoop callback, which fires for every ToolsNode invocation and
// avoids duplicating spans.
func NewCollectEvidenceTool(cfg TargetConfig, audit AuditSink, runID string, gw *ToolGateway) (tool.InvokableTool, error) {
	return utils.InferTool(
		ToolCollectEvidence,
		"Collects a baseline evidence bundle for the configured target service: HTTP probe results for the liveness/readiness/business endpoints, aggregated service error logs, and app/redis container status. Returns a structured evidence object. Call this first before concluding; use the finer-grained tools only for targeted follow-up.",
		func(ctx context.Context, _ collectEvidenceInput) (*toolEnvelope, error) {
			return gw.invoke(ctx, ToolCollectEvidence, nil, func(ctx context.Context) (any, *model.ToolError) {
				collected, err := CollectEvidence(ctx, runID, cfg, audit)
				if err != nil {
					return nil, &model.ToolError{Type: model.ErrorTypeInternal, Message: err.Error()}
				}
				return collected.Evidence, nil
			}), nil
		},
	)
}

// AgentTools returns the tools exposed to the ReAct agent: the deterministic
// baseline evidence tool plus the atomic tools for targeted investigation.
// All tools — including the composite collect_service_evidence — are wired
// through the same ToolGateway so every model-driven call is audited
// uniformly. Tool spans are emitted by the Eino ADK CozeLoop callback.
func AgentTools(cfg TargetConfig, audit AuditSink, runID string) ([]tool.InvokableTool, error) {
	gw := NewToolGateway(audit, runID)
	atomic, err := RegisteredTools(gw)
	if err != nil {
		return nil, err
	}
	evidence, err := NewCollectEvidenceTool(cfg, audit, runID, gw)
	if err != nil {
		return nil, err
	}
	// Evidence tool first so it appears first in the tool list.
	return append([]tool.InvokableTool{evidence}, atomic...), nil
}
