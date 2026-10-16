package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestBuildFixedStepsHTTPTimeoutPropagation asserts the configured HTTP
// timeout reaches the tool call under the exact httpToolInput JSON key
// (timeout_ms), while the collector-only label stays in audit args and is
// never sent to the tool.
func TestBuildFixedStepsHTTPTimeoutPropagation(t *testing.T) {
	cfg := TargetConfig{
		BaseURL:        "http://127.0.0.1:8080",
		AppContainer:   "ial-demo-api",
		RedisContainer: "ial-redis",
		HTTPTimeoutMS:  1234,
		LogTailLines:   77,
	}
	steps := buildFixedSteps(cfg, "run-timeout", nil).toolSteps()

	httpCount := 0
	for _, s := range steps {
		if s.Name != ToolCheckHTTP {
			continue
		}
		httpCount++
		if got := s.Arguments["timeout_ms"]; got != 1234 {
			t.Fatalf("http step %d: Arguments[timeout_ms] = %v, want 1234 (args=%v)", httpCount, got, s.Arguments)
		}
		if _, ok := s.Arguments["label"]; ok {
			t.Fatalf("http step %d: label must not be sent to the tool, args=%v", httpCount, s.Arguments)
		}
		if s.Arguments["url"] == nil || s.Arguments["url"] == "" {
			t.Fatalf("http step %d: missing url in tool args: %v", httpCount, s.Arguments)
		}
		if got := s.AuditArgs["timeout_ms"]; got != 1234 {
			t.Fatalf("http step %d: AuditArgs[timeout_ms] = %v, want 1234", httpCount, got)
		}
		if label, _ := s.AuditArgs["label"].(string); label == "" {
			t.Fatalf("http step %d: AuditArgs must keep the label, got %v", httpCount, s.AuditArgs)
		}
		if s.AuditArgs["url"] != s.Arguments["url"] {
			t.Fatalf("http step %d: audit url %v != tool url %v", httpCount, s.AuditArgs["url"], s.Arguments["url"])
		}
	}
	if httpCount != 3 {
		t.Fatalf("want 3 http steps, got %d", httpCount)
	}
}

// TestBuildFixedStepsLogsTailKeyPropagation asserts the configured tail
// line count reaches the tool under the logsToolInput JSON key tail_lines
// and the stale "tail" key is gone.
func TestBuildFixedStepsLogsTailKeyPropagation(t *testing.T) {
	cfg := TargetConfig{
		BaseURL:        "http://127.0.0.1:8080",
		AppContainer:   "ial-demo-api",
		RedisContainer: "ial-redis",
		HTTPTimeoutMS:  1234,
		LogTailLines:   77,
	}
	steps := buildFixedSteps(cfg, "run-tail", nil).toolSteps()

	var logStep *ToolStep
	for i := range steps {
		if steps[i].Name == ToolServiceLogs {
			logStep = &steps[i]
			break
		}
	}
	if logStep == nil {
		t.Fatal("workflow has no query_service_logs step")
	}
	if got := logStep.Arguments["tail_lines"]; got != 77 {
		t.Fatalf("Arguments[tail_lines] = %v, want 77 (args=%v)", got, logStep.Arguments)
	}
	if _, ok := logStep.Arguments["tail"]; ok {
		t.Fatalf("stale \"tail\" key must not be sent to the tool: %v", logStep.Arguments)
	}
	if got := logStep.Arguments["service"]; got != "ial-demo-api" {
		t.Fatalf("Arguments[service] = %v, want ial-demo-api", got)
	}
}

// TestServiceLogsToolRejectsTailOverLimit proves the >5000 guard is now
// reachable: with tail_lines propagated correctly the tool returns an
// invalid_input envelope instead of silently falling back to the default.
func TestServiceLogsToolRejectsTailOverLimit(t *testing.T) {
	tl, err := NewServiceLogsTool(nil)
	if err != nil {
		t.Fatalf("register tool: %v", err)
	}
	out, err := tl.InvokableRun(context.Background(), `{"service":"x","tail_lines":9999}`)
	if err != nil {
		t.Fatalf("tool must envelope validation failures, got err: %v", err)
	}
	var env toolEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("invalid envelope: %v (%s)", err, out)
	}
	if env.OK {
		t.Fatalf("tail_lines=9999 must be rejected, got ok envelope: %s", out)
	}
	if env.ErrorType != "invalid_input" {
		t.Fatalf("want invalid_input for tail_lines=9999, got %q (%s)", env.ErrorType, out)
	}
}

// TestCollectEvidenceDefaultPropagationAndAudit runs the full workflow
// against a stub HTTP server with a custom config and asserts: HTTP steps
// carry the configured timeout, audit records keep url/label for HTTP
// steps (and label for container steps), and the audit JSONL persists
// those keys after redaction.
func TestCollectEvidenceDefaultPropagationAndAudit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/ready" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"summary":"redis unavailable"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	audit := NewAuditLog(auditPath)
	cfg := DefaultTarget()
	cfg.BaseURL = srv.URL

	res, err := CollectEvidence(context.Background(), "run-e2e", cfg, audit)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(res.Trace) != 6 {
		t.Fatalf("want 6 trace entries, got %+v", res.Trace)
	}
	if res.Evidence.Health == nil || res.Evidence.Health.StatusCode != 200 {
		t.Fatalf("health should be 200: %+v", res.Evidence.Health)
	}
	if res.Evidence.Ready == nil || res.Evidence.Ready.StatusCode != 503 {
		t.Fatalf("ready should be 503: %+v", res.Evidence.Ready)
	}

	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	lines := 0
	sawHTTP := false
	sawContainer := false
	sawLogs := false
	for _, line := range splitLines(raw) {
		var rec struct {
			Tool string         `json:"tool"`
			Args map[string]any `json:"args"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("audit line not json: %v (%s)", err, line)
		}
		lines++
		switch rec.Tool {
		case ToolCheckHTTP:
			sawHTTP = true
			if rec.Args["url"] == nil {
				t.Fatalf("http audit args must keep url: %v", rec.Args)
			}
			if rec.Args["label"] == nil {
				t.Fatalf("http audit args must keep label: %v", rec.Args)
			}
			if rec.Args["timeout_ms"] != float64(DefaultTarget().HTTPTimeoutMS) {
				t.Fatalf("http audit args timeout_ms = %v, want %d", rec.Args["timeout_ms"], DefaultTarget().HTTPTimeoutMS)
			}
		case ToolContainerGet:
			sawContainer = true
			if rec.Args["label"] == nil || rec.Args["container"] == nil {
				t.Fatalf("container audit args must keep label/container: %v", rec.Args)
			}
		case ToolServiceLogs:
			sawLogs = true
			if rec.Args["tail_lines"] != float64(DefaultTarget().LogTailLines) {
				t.Fatalf("logs audit args tail_lines = %v, want %d", rec.Args["tail_lines"], DefaultTarget().LogTailLines)
			}
			if _, ok := rec.Args["tail"]; ok {
				t.Fatalf("logs audit args must not use stale tail key: %v", rec.Args)
			}
		}
	}
	if lines != 6 || !sawHTTP || !sawContainer || !sawLogs {
		t.Fatalf("audit jsonl incomplete: lines=%d http=%v logs=%v container=%v", lines, sawHTTP, sawLogs, sawContainer)
	}
}

func splitLines(raw []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range raw {
		if b == '\n' {
			if i > start {
				out = append(out, raw[start:i])
			}
			start = i + 1
		}
	}
	if start < len(raw) {
		out = append(out, raw[start:])
	}
	return out
}
