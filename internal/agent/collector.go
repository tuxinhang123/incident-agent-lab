package agent

import (
	"time"

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
}

// DefaultTarget returns the V0 Docker Compose target names.
func DefaultTarget() TargetConfig {
	return TargetConfig{
		BaseURL:        "http://127.0.0.1:8080",
		AppContainer:   "ial-demo-api",
		RedisContainer: "ial-redis",
		HTTPTimeoutMS:  3000,
		LogTailLines:   200,
	}
}

// AuditSink records every tool invocation. Record is called once per tool.
type AuditSink interface {
	Record(runID, tool string, args map[string]any, started time.Time, result any, toolErr *model.ToolError) error
}

// CollectEvidence runs the fixed workflow order from the design doc:
// HTTP endpoints -> app error logs -> dependency/app container status.
// Tool failures never abort the run; they become observations so the
// diagnoser can report insufficient_evidence.
func CollectEvidence(runID string, cfg TargetConfig, audit AuditSink) *model.CollectedEvidence {
	ev := &model.CollectedEvidence{}

	// 1. HTTP endpoints: readiness first (dependency check), then business.
	ev.Health = observeHTTP(runID, audit, "/health", cfg.BaseURL+"/health", cfg.HTTPTimeoutMS)
	ev.Ready = observeHTTP(runID, audit, "/ready", cfg.BaseURL+"/ready", cfg.HTTPTimeoutMS)
	ev.Data = observeHTTP(runID, audit, "/api/data", cfg.BaseURL+"/api/data", cfg.HTTPTimeoutMS)

	// 2. Aggregated application error logs.
	ev.AppLogs = observeLogs(runID, audit, cfg.AppContainer, cfg.LogTailLines)

	// 3. Dependency container status, then the app container itself.
	ev.RedisStatus = observeContainer(runID, audit, "redis", cfg.RedisContainer)
	ev.AppStatus = observeContainer(runID, audit, "app", cfg.AppContainer)

	return ev
}

func observeHTTP(runID string, audit AuditSink, label, url string, timeoutMS int) *model.HTTPCheckObservation {
	in := tools.HTTPEndpointInput{URL: url, TimeoutMS: timeoutMS}
	started := time.Now()
	res, tErr := tools.CheckHTTPEndpoint(in)
	auditRecord(audit, runID, ToolCheckHTTP, map[string]any{"url": url, "label": label}, started, res, tErr)
	o := &model.HTTPCheckObservation{Source: ToolCheckHTTP}
	if tErr != nil {
		o.OK = false
		o.ErrorType = tErr.Type
		o.Summary = tErr.Message
		return o
	}
	o.OK = true
	o.StatusCode = res.StatusCode
	o.Summary = res.BodySummary
	return o
}

func observeLogs(runID string, audit AuditSink, service string, tail int) *model.LogObservation {
	in := tools.ServiceLogsInput{Service: service, Tail: tail}
	started := time.Now()
	res, tErr := tools.QueryServiceLogs(in)
	auditRecord(audit, runID, ToolServiceLogs, map[string]any{"service": service, "tail": tail}, started, res, tErr)
	o := &model.LogObservation{Source: ToolServiceLogs}
	if tErr != nil {
		o.OK = false
		o.ErrorType = tErr.Type
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

func observeContainer(runID string, audit AuditSink, label, container string) *model.ContainerObservation {
	in := tools.ContainerStatusInput{Container: container}
	started := time.Now()
	res, tErr := tools.GetContainerStatus(in)
	auditRecord(audit, runID, ToolContainerGet, map[string]any{"container": container, "label": label}, started, res, tErr)
	o := &model.ContainerObservation{Source: ToolContainerGet}
	if tErr != nil {
		o.OK = false
		o.ErrorType = tErr.Type
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

func auditRecord(audit AuditSink, runID, tool string, args map[string]any, started time.Time, result any, tErr *model.ToolError) {
	if audit == nil {
		return
	}
	_ = audit.Record(runID, tool, args, started, result, tErr)
}
