package agent

import (
	"context"
	"fmt"
	"os"
	"time"

	cmodel "github.com/cloudwego/eino/components/model"
	"github.com/coze-dev/cozeloop-go/spec/tracespec"

	diagmodel "github.com/tuxinhang123/incident-agent-lab/internal/model"
	"github.com/tuxinhang123/incident-agent-lab/internal/observability"
)

// Span type names reported to Coze Loop.
const (
	spanTypeWorkflow = "workflow"
	spanTypeModel    = tracespec.VModelSpanType
	spanTypeTool     = tracespec.VToolSpanType
)

// Report is the complete diagnose run: evidence, the final diagnosis,
// how it was produced and the audited execution trace.
type Report struct {
	RunID           string                       `json:"run_id"`
	GeneratedAt     time.Time                    `json:"generated_at"`
	DiagnosisSource string                       `json:"diagnosis_source"` // "model" | "rules"
	Evidence        *diagmodel.CollectedEvidence `json:"evidence"`
	Diagnosis       *diagmodel.Diagnosis         `json:"diagnosis"`
	Trace           []TraceEntry                 `json:"trace"`
}

// Run executes the diagnose workflow:
//   - With a chat model: a ReAct agent loop where the model autonomously
//     decides which tools to call, gathers evidence, and emits a final
//     diagnosis. On failure or invalid output it falls back to rules.
//   - Without a model: the deterministic fixed-workflow collector plus the
//     rule-based diagnoser.
func Run(ctx context.Context, runID string, cfg TargetConfig, chat cmodel.ChatModel, modelName string, audit AuditSink, tr observability.Tracer) (*Report, error) {
	var root observability.Span
	if tr != nil {
		ctx, root = tr.StartSpan(ctx, "incident.diagnose", spanTypeWorkflow,
			map[string]any{"run_id": runID}, map[string]any{"target": cfg.BaseURL})
		defer func() { root.Finish(ctx) }()
	}

	report := &Report{
		RunID:       runID,
		GeneratedAt: time.Now().UTC(),
	}

	if chat != nil {
		d, trace, err := RunAgenticLoop(ctx, chat, modelName, cfg, audit, tr, runID)
		if err == nil && d != nil && validateAgentDiagnosis(d) {
			report.Diagnosis = d
			report.DiagnosisSource = "model"
			report.Trace = trace
			if root != nil {
				root.SetTags(ctx, map[string]any{"diagnosis_source": "model", "root_cause": d.RootCause})
				root.SetOutput(ctx, d)
			}
			return report, nil
		}
		fmt.Fprintf(os.Stderr, "warning: agent loop did not produce a valid diagnosis (%v); falling back to rules\n", err)
	}

	collected, err := CollectEvidence(ctx, runID, cfg, audit, tr)
	if err != nil {
		if root != nil {
			root.SetError(ctx, err)
			root.SetStatusCode(ctx, 1)
		}
		return nil, err
	}
	report.Evidence = collected.Evidence
	report.Trace = collected.Trace
	report.Diagnosis = DiagnoseRules(collected.Evidence)
	report.DiagnosisSource = "rules"
	if root != nil {
		root.SetTags(ctx, map[string]any{"diagnosis_source": "rules", "root_cause": report.Diagnosis.RootCause})
		root.SetOutput(ctx, report.Diagnosis)
	}
	return report, nil
}

// validateAgentDiagnosis performs lightweight structural validation on a
// diagnosis produced by the ReAct loop. It does not cross-check against a
// pre-collected evidence bag (the model gathered its own evidence); it only
// ensures the taxonomy and status fields are well-formed.
func validateAgentDiagnosis(d *diagmodel.Diagnosis) bool {
	if d == nil || d.RootCause == "" {
		return false
	}
	validCauses := map[string]bool{
		diagmodel.CauseRedisNotRunning: true,
		diagmodel.CauseAppStopped:      true,
		diagmodel.CauseAppTimeout:      true,
		diagmodel.CauseNone:            true,
		diagmodel.CauseUnknown:         true,
	}
	if !validCauses[d.RootCause] {
		return false
	}
	validStatuses := map[string]bool{
		diagmodel.StatusDiagnosed:            true,
		diagmodel.StatusInsufficientEvidence: true,
		diagmodel.StatusHealthy:              true,
	}
	if !validStatuses[d.Status] {
		return false
	}
	if d.Confidence < 0 || d.Confidence > 1 {
		return false
	}
	if d.SuggestedActions == nil {
		d.SuggestedActions = []string{}
	}
	return true
}
