package agent

import (
	"context"
	"fmt"
	"os"
	"time"

	cmodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
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

// Run executes the V0 fixed workflow: collect evidence in fixed order via
// the Eino ToolsNode (each step reported to Coze Loop when configured),
// then diagnose (model when available and rule-validated, otherwise rules).
func Run(ctx context.Context, runID string, cfg TargetConfig, chat cmodel.ChatModel, modelName string, audit AuditSink, tr observability.Tracer) (*Report, error) {
	var root observability.Span
	if tr != nil {
		ctx, root = tr.StartSpan(ctx, "incident.diagnose", spanTypeWorkflow,
			map[string]any{"run_id": runID}, map[string]any{"target": cfg.BaseURL})
		defer func() { root.Finish(ctx) }()
	}

	collected, err := CollectEvidence(ctx, runID, cfg, audit, tr)
	if err != nil {
		if root != nil {
			root.SetError(ctx, err)
			root.SetStatusCode(ctx, 1)
		}
		return nil, err
	}

	report := &Report{
		RunID:       runID,
		GeneratedAt: time.Now().UTC(),
		Evidence:    collected.Evidence,
		Trace:       collected.Trace,
	}

	if chat != nil {
		d, ok := diagnoseWithModelTraced(tr, chat, modelName, collected.Evidence)
		if ok && d != nil {
			report.Diagnosis = d
			report.DiagnosisSource = "model"
			if root != nil {
				root.SetTags(ctx, map[string]any{"diagnosis_source": "model", "root_cause": d.RootCause})
				root.SetOutput(ctx, d)
			}
			return report, nil
		}
	}

	report.Diagnosis = DiagnoseRules(collected.Evidence)
	report.DiagnosisSource = "rules"
	if root != nil {
		root.SetTags(ctx, map[string]any{"diagnosis_source": "rules", "root_cause": report.Diagnosis.RootCause})
		root.SetOutput(ctx, report.Diagnosis)
	}
	return report, nil
}

// diagnoseWithModelTraced wraps the optional LLM call in a Coze Loop
// "model" span carrying prompt/output and token usage when present.
func diagnoseWithModelTraced(tr observability.Tracer, chat cmodel.ChatModel, modelName string, ev *diagmodel.CollectedEvidence) (*diagmodel.Diagnosis, bool) {
	rawEvidence := mustJSON(ev)
	messages := []*schema.Message{
		schema.SystemMessage(diagnoseSystemPrompt),
		schema.UserMessage("Collected evidence JSON:\n" + string(rawEvidence)),
	}

	// Reasoning models may still take 15-30s on larger evidence sets even at
	// "low" reasoning effort, so allow up to modelCallTimeout by default,
	// overridable via MODEL_TIMEOUT.
	timeout := modelCallTimeout
	if v := os.Getenv("MODEL_TIMEOUT"); v != "" {
		if d, perr := time.ParseDuration(v); perr == nil {
			timeout = d
		}
	}
	// Root the model call in a fresh, independently-budgeted context. The
	// caller's ctx carries the whole-workflow deadline, whose remaining budget
	// is already eaten into by evidence collection (container/log/HTTP probes),
	// which would otherwise truncate a slow reasoning-model response.
	callCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if tr == nil {
		out, err := chat.Generate(callCtx, messages)
		if err != nil || out == nil {
			fmt.Fprintf(os.Stderr, "warning: failed to call model: %v\n", err)
			return nil, false
		}
		d := parseModelDiagnosis(out.Content)
		if d == nil {
			return nil, false
		}
		return Validate(d, ev)
	}

	spanCtx, span := tr.StartSpan(callCtx, "diagnose.llm", spanTypeModel,
		map[string]any{"model_provider": "openai_compatible"},
		map[string]any{"messages": messageContents(messages)})
	if modelName != "" {
		span.SetModelName(spanCtx, modelName)
	}
	out, err := chat.Generate(spanCtx, messages)
	if err != nil || out == nil {
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to call model: %v\n", err)
			span.SetError(spanCtx, err)
			span.SetStatusCode(spanCtx, 1)
		}
		span.Finish(spanCtx)
		return nil, false
	}
	d := parseModelDiagnosis(out.Content)
	if d == nil {
		span.SetStatusCode(spanCtx, 1)
		span.SetOutput(spanCtx, map[string]any{"raw": out.Content, "parse": "failed"})
		span.Finish(spanCtx)
		return nil, false
	}
	validated, ok := Validate(d, ev)
	span.SetOutput(spanCtx, d)
	if out.ResponseMeta != nil && out.ResponseMeta.Usage != nil {
		u := out.ResponseMeta.Usage
		span.SetInputTokens(spanCtx, u.PromptTokens)
		span.SetOutputTokens(spanCtx, u.CompletionTokens)
		span.SetTags(spanCtx, map[string]any{"total_tokens": u.TotalTokens})
	}
	if !ok {
		span.SetTags(spanCtx, map[string]any{"validation": "rejected_fallback_rules"})
	}
	span.Finish(spanCtx)
	return validated, ok
}

func messageContents(ms []*schema.Message) []map[string]string {
	out := make([]map[string]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, map[string]string{"role": string(m.Role), "content": m.Content})
	}
	return out
}
