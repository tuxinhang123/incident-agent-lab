package agent

import (
	"context"
	"time"

	cmodel "github.com/cloudwego/eino/components/model"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

// Report is the complete diagnose run: evidence, the final diagnosis and
// how it was produced.
type Report struct {
	RunID           string                   `json:"run_id"`
	GeneratedAt     time.Time                `json:"generated_at"`
	DiagnosisSource string                   `json:"diagnosis_source"` // "model" | "rules"
	Evidence        *model.CollectedEvidence `json:"evidence"`
	Diagnosis       *model.Diagnosis         `json:"diagnosis"`
}

// Run executes the V0 fixed workflow: collect evidence in fixed order, then
// diagnose (model when available and rule-validated, otherwise rules).
func Run(ctx context.Context, runID string, cfg TargetConfig, chat cmodel.ChatModel, audit AuditSink) *Report {
	evidence := CollectEvidence(runID, cfg, audit)

	report := &Report{
		RunID:       runID,
		GeneratedAt: time.Now().UTC(),
		Evidence:    evidence,
	}

	if chat != nil {
		if d, ok := DiagnoseWithModel(ctx, chat, evidence); ok && d != nil {
			report.Diagnosis = d
			report.DiagnosisSource = "model"
			return report
		}
	}

	report.Diagnosis = DiagnoseRules(evidence)
	report.DiagnosisSource = "rules"
	return report
}
