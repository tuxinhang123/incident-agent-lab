package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cloudwego/eino/components/tool"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
	"github.com/tuxinhang123/incident-agent-lab/internal/tools"
)

// TargetConfig identifies the diagnosed service and its dependency.
type TargetConfig struct {
	BaseURL        string
	AppContainer   string
	RedisContainer string
	HTTPTimeoutMS  int
	LogTailLines   int
	// Probes are the HTTP paths probed during baseline evidence collection,
	// in order. Position 0 = liveness (-> Health), 1 = readiness (-> Ready),
	// 2 = business (-> Data). The rule-based diagnoser consumes exactly these
	// three roles; extra positions are reserved for future generalized
	// evidence and ignored by rules. Empty falls back to DefaultProbes.
	Probes []string
}

// Default probe paths for the V0 demo-api target. They are the default value
// of TargetConfig.Probes; callers may override them to adapt to services with
// different liveness/readiness/business endpoints.
const (
	PathHealth = "/health"
	PathReady  = "/ready"
	PathData   = "/api/data"
)

// DefaultProbes returns the default three-role probe path list.
func DefaultProbes() []string {
	return []string{PathHealth, PathReady, PathData}
}

// DefaultTarget returns the V0 Docker Compose target names.
func DefaultTarget() TargetConfig {
	return TargetConfig{
		BaseURL:        "http://127.0.0.1:8080",
		AppContainer:   "ial-demo-api",
		RedisContainer: "ial-redis",
		HTTPTimeoutMS:  3000,
		LogTailLines:   200,
		Probes:         DefaultProbes(),
	}
}

// probes returns the configured probe list, falling back to the defaults.
func (c TargetConfig) probes() []string {
	if len(c.Probes) == 0 {
		return DefaultProbes()
	}
	return c.Probes
}

// AuditSink records every tool invocation. Record is called once per tool.
type AuditSink interface {
	Record(runID, tool string, args map[string]any, started time.Time, result any, toolErr *model.ToolError) error
}

// CollectResult bundles the gathered evidence with the execution trace.
type CollectResult struct {
	Evidence *model.CollectedEvidence
	Trace    []TraceEntry
}

// observationBag is filled by per-step delivery callbacks while the fixed
// workflow runs through the Eino ToolsNode.
type observationBag struct {
	ready   *model.HTTPCheckObservation
	data    *model.HTTPCheckObservation
	health  *model.HTTPCheckObservation
	appLogs *model.LogObservation
	redis   *model.ContainerObservation
	app     *model.ContainerObservation
}

func (b *observationBag) toEvidence() *model.CollectedEvidence {
	return &model.CollectedEvidence{
		Ready:       b.ready,
		Data:        b.data,
		Health:      b.health,
		AppLogs:     b.appLogs,
		RedisStatus: b.redis,
		AppStatus:   b.app,
	}
}

// CollectEvidence runs the fixed workflow via the Eino ToolsNode:
// HTTP endpoints -> app error logs -> dependency/app container status.
// Tool failures never abort the run; they become observations so the
// diagnoser can report insufficient_evidence.
func CollectEvidence(ctx context.Context, runID string, cfg TargetConfig, audit AuditSink) (*CollectResult, error) {
	registered, err := RegisteredTools(nil)
	if err != nil {
		return nil, err
	}
	baseTools := make([]tool.BaseTool, len(registered))
	for i, t := range registered {
		baseTools[i] = t
	}

	steps := buildFixedSteps(cfg, runID, audit)
	node, err := NewFixedWorkflowToolNode(ctx, baseTools, steps.toolSteps())
	if err != nil {
		return nil, err
	}
	bag, trace, err := node.Run(ctx)
	if err != nil {
		return nil, err
	}
	return &CollectResult{Evidence: bag.toEvidence(), Trace: trace}, nil
}

// stepSpec is a ToolStep plus its start timestamp, captured before the
// ToolsNode runs the call so latency includes only tool execution.
type stepSpec struct {
	step    ToolStep
	started time.Time
}

// fixedSteps holds the ordered deterministic workflow steps.
type fixedSteps struct {
	items []stepSpec
}

func buildFixedSteps(cfg TargetConfig, runID string, audit AuditSink) *fixedSteps {
	fs := &fixedSteps{}

	httpStep := func(label, url string, sink func(*model.HTTPCheckObservation, *observationBag)) {
		started := nowFn()
		// Arguments carries only the tool's schema fields; the label is
		// collector-only metadata and stays out of the tool call.
		toolArgs := map[string]any{"url": url, "timeout_ms": cfg.HTTPTimeoutMS}
		auditArgs := map[string]any{"url": url, "label": label, "timeout_ms": cfg.HTTPTimeoutMS}
		fs.items = append(fs.items, stepSpec{
			started: started,
			step: ToolStep{
				Name:      ToolCheckHTTP,
				Arguments: toolArgs,
				AuditArgs: auditArgs,
				Deliver: func(env *toolEnvelope, raw json.RawMessage, bag *observationBag) {
					auditEnvelope(audit, runID, ToolCheckHTTP, auditArgs, started, env, raw)
					sink(httpObservation(env, raw), bag)
				},
			},
		})
	}

	// Probes are bound to the three rule-consumed roles by position:
	// 0 = liveness (Health), 1 = readiness (Ready), 2 = business (Data).
	// Missing positions leave the corresponding observation nil.
	roleSinks := []func(*model.HTTPCheckObservation, *observationBag){
		func(o *model.HTTPCheckObservation, bag *observationBag) { bag.health = o },
		func(o *model.HTTPCheckObservation, bag *observationBag) { bag.ready = o },
		func(o *model.HTTPCheckObservation, bag *observationBag) { bag.data = o },
	}
	for i, path := range cfg.probes() {
		if i >= len(roleSinks) {
			break
		}
		httpStep(path, cfg.BaseURL+path, roleSinks[i])
	}

	{
		started := nowFn()
		args := map[string]any{"service": cfg.AppContainer, "tail_lines": cfg.LogTailLines}
		fs.items = append(fs.items, stepSpec{
			started: started,
			step: ToolStep{
				Name: ToolServiceLogs, Arguments: args,
				Deliver: func(env *toolEnvelope, raw json.RawMessage, bag *observationBag) {
					auditEnvelope(audit, runID, ToolServiceLogs, args, started, env, raw)
					bag.appLogs = logsObservation(env, raw)
				},
			},
		})
	}
	containerStep := func(label, name string, sink func(*model.ContainerObservation, *observationBag)) {
		started := nowFn()
		toolArgs := map[string]any{"container": name}
		auditArgs := map[string]any{"container": name, "label": label}
		fs.items = append(fs.items, stepSpec{
			started: started,
			step: ToolStep{
				Name: ToolContainerGet, Arguments: toolArgs, AuditArgs: auditArgs,
				Deliver: func(env *toolEnvelope, raw json.RawMessage, bag *observationBag) {
					auditEnvelope(audit, runID, ToolContainerGet, auditArgs, started, env, raw)
					sink(containerObservation(env, raw), bag)
				},
			},
		})
	}
	containerStep("redis", cfg.RedisContainer, func(o *model.ContainerObservation, bag *observationBag) {
		bag.redis = o
	})
	containerStep("app", cfg.AppContainer, func(o *model.ContainerObservation, bag *observationBag) {
		bag.app = o
	})

	return fs
}

func (fs *fixedSteps) toolSteps() []ToolStep {
	out := make([]ToolStep, len(fs.items))
	for i, s := range fs.items {
		out[i] = s.step
	}
	return out
}

func httpObservation(env *toolEnvelope, raw json.RawMessage) *model.HTTPCheckObservation {
	o := &model.HTTPCheckObservation{Source: ToolCheckHTTP}
	if env == nil || !env.OK {
		o.OK = false
		if env != nil {
			o.ErrorType = env.ErrorType
			o.Summary = env.ErrorMsg
		}
		return o
	}
	var res tools.HTTPEndpointResult
	if err := json.Unmarshal(raw, &res); err != nil {
		o.OK = false
		o.ErrorType = model.ErrorTypeInternal
		o.Summary = err.Error()
		return o
	}
	o.OK = true
	o.StatusCode = res.StatusCode
	o.Summary = res.BodySummary
	return o
}

func logsObservation(env *toolEnvelope, raw json.RawMessage) *model.LogObservation {
	o := &model.LogObservation{Source: ToolServiceLogs}
	if env == nil || !env.OK {
		o.OK = false
		if env != nil {
			o.ErrorType = env.ErrorType
		}
		return o
	}
	var res tools.ServiceLogsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		o.OK = false
		o.ErrorType = model.ErrorTypeInternal
		return o
	}
	o.OK = true
	o.LinesScanned = res.LinesScanned
	for _, e := range res.MatchedErrors {
		o.Errors = append(o.Errors, model.ErrorAggregate{
			ErrorType: e.ErrorType, Count: e.Count, Sample: e.Sample,
		})
	}
	return o
}

func containerObservation(env *toolEnvelope, raw json.RawMessage) *model.ContainerObservation {
	o := &model.ContainerObservation{Source: ToolContainerGet}
	if env == nil || !env.OK {
		o.OK = false
		if env != nil {
			o.ErrorType = env.ErrorType
		}
		return o
	}
	var res tools.ContainerStatusResult
	if err := json.Unmarshal(raw, &res); err != nil {
		o.OK = false
		o.ErrorType = model.ErrorTypeInternal
		return o
	}
	o.OK = true
	o.Status = res.Status
	o.Running = res.Running
	if res.ExitCode != nil {
		exit := *res.ExitCode
		o.ExitCode = &exit
	}
	return o
}

// auditEnvelope replays one typed tool call into the audit sink.
func auditEnvelope(audit AuditSink, runID, name string, args map[string]any, started time.Time, env *toolEnvelope, raw json.RawMessage) {
	if audit == nil || env == nil {
		return
	}
	var result any
	switch name {
	case ToolCheckHTTP:
		var r tools.HTTPEndpointResult
		if env.OK && json.Unmarshal(raw, &r) == nil {
			result = &r
		}
	case ToolServiceLogs:
		var r tools.ServiceLogsResult
		if env.OK && json.Unmarshal(raw, &r) == nil {
			result = &r
		}
	case ToolContainerGet:
		var r tools.ContainerStatusResult
		if env.OK && json.Unmarshal(raw, &r) == nil {
			result = &r
		}
	}
	var tErr *model.ToolError
	if !env.OK {
		tErr = &model.ToolError{Type: env.ErrorType, Message: env.ErrorMsg}
	}
	_ = audit.Record(runID, name, args, started, result, tErr)
}
