package agent

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
	"github.com/tuxinhang123/incident-agent-lab/internal/observability"
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
func NewHTTPCheckTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		ToolCheckHTTP,
		"Performs a GET and returns HTTP status code, latency_ms and a truncated body_summary. Non-2xx responses are reported in the result, not as errors.",
		func(_ context.Context, in httpToolInput) (*toolEnvelope, error) {
			res, tErr := tools.CheckHTTPEndpoint(tools.HTTPEndpointInput{URL: in.URL, TimeoutMS: in.TimeoutMS})
			if tErr != nil {
				return errorEnvelope(tErr), nil
			}
			return &toolEnvelope{OK: true, Result: res}, nil
		},
	)
}

// NewContainerStatusTool registers get_container_status as an Eino tool.
func NewContainerStatusTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		ToolContainerGet,
		"Returns a Docker container's status, running flag, exit code and timing information from docker inspect.",
		func(_ context.Context, in containerToolInput) (*toolEnvelope, error) {
			res, tErr := tools.GetContainerStatus(tools.ContainerStatusInput{Container: in.Container})
			if tErr != nil {
				return errorEnvelope(tErr), nil
			}
			return &toolEnvelope{OK: true, Result: res}, nil
		},
	)
}

// NewServiceLogsTool registers query_service_logs as an Eino tool.
func NewServiceLogsTool() (tool.InvokableTool, error) {
	return utils.InferTool(
		ToolServiceLogs,
		"Reads recent container logs and aggregates structured error entries by error_type with counts and one sample each; raw logs are never returned.",
		func(_ context.Context, in logsToolInput) (*toolEnvelope, error) {
			res, tErr := tools.QueryServiceLogs(tools.ServiceLogsInput{Service: in.Service, Tail: in.TailLines})
			if tErr != nil {
				return errorEnvelope(tErr), nil
			}
			return &toolEnvelope{OK: true, Result: res}, nil
		},
	)
}

// RegisteredTools returns all V0 read-only tools in fixed workflow order.
func RegisteredTools() ([]tool.InvokableTool, error) {
	ctors := []func() (tool.InvokableTool, error){
		NewHTTPCheckTool,
		NewServiceLogsTool,
		NewContainerStatusTool,
	}
	out := make([]tool.InvokableTool, 0, len(ctors))
	for _, ctor := range ctors {
		t, err := ctor()
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
func NewCollectEvidenceTool(cfg TargetConfig, audit AuditSink, tr observability.Tracer, runID string) (tool.InvokableTool, error) {
	return utils.InferTool(
		ToolCollectEvidence,
		"Collects a baseline evidence bundle for the configured target service: HTTP probe results for the liveness/readiness/business endpoints, aggregated service error logs, and app/redis container status. Returns a structured evidence object. Call this first before concluding; use the finer-grained tools only for targeted follow-up.",
		func(ctx context.Context, _ collectEvidenceInput) (*toolEnvelope, error) {
			collected, err := CollectEvidence(ctx, runID, cfg, audit, tr)
			if err != nil {
				return &toolEnvelope{OK: false, ErrorType: model.ErrorTypeInternal, ErrorMsg: err.Error()}, nil
			}
			return &toolEnvelope{OK: true, Result: collected.Evidence}, nil
		},
	)
}

// AgentTools returns the tools exposed to the ReAct agent: the deterministic
// baseline evidence tool plus the atomic tools for targeted investigation.
func AgentTools(cfg TargetConfig, audit AuditSink, tr observability.Tracer, runID string) ([]tool.InvokableTool, error) {
	atomic, err := RegisteredTools()
	if err != nil {
		return nil, err
	}
	evidence, err := NewCollectEvidenceTool(cfg, audit, tr, runID)
	if err != nil {
		return nil, err
	}
	// Evidence tool first so it appears first in the tool list.
	return append([]tool.InvokableTool{evidence}, atomic...), nil
}

func errorEnvelope(e *model.ToolError) *toolEnvelope {
	return &toolEnvelope{OK: false, ErrorType: e.Type, ErrorMsg: e.Message}
}
