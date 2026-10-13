package agent

import (
	"context"
	"time"

	cmodel "github.com/cloudwego/eino/components/model"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

// Report is the complete diagnose run: evidence, the final diagnosis,
// how it was produced and the audited execution trace.
type Report struct {
	RunID           string                   `json:"run_id"`
	GeneratedAt     time.Time                `json:"generated_at"`
	DiagnosisSource string                   `json:"diagnosis_source"` // "model" | "rules"
	Evidence        *model.CollectedEvidence `json:"evidence"`
	Diagnosis       *model.Diagnosis         `json:"diagnosis"`
	Trace           []TraceEntry             `json:"trace"`
}

// Run executes the V0 fixed workflow: collect evidence in fixed order via
// the Eino ToolsNode, then diagnose (model when available and
// rule-validated, otherwise rules).
func Run(ctx context.Context, runID string, cfg TargetConfig, chat cmodel.ChatModel, audit AuditSink) (*Report, error) {
	collected, err := CollectEvidence(ctx, runID, cfg, audit)
	if err != nil {
		return nil, err
	}

	report := &Report{
		RunID:       runID,
		GeneratedAt: time.Now().UTC(),
		Evidence:    collected.Evidence,
		Trace:       collected.Trace,
	}

	if chat != nil {
		if d, ok := DiagnoseWithModel(ctx, chat, collected.Evidence); ok && d != nil {
			report.Diagnosis = d
			report.DiagnosisSource = "model"
			return report, nil
		}
	}

	report.Diagnosis = DiagnoseRules(collected.Evidence)
	report.DiagnosisSource = "rules"
	return report, nil
}
