package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	cmodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
	"github.com/tuxinhang123/incident-agent-lab/internal/observability"
)

// fakeTracer records the spans a run opens without sending anything over
// the network.
type fakeTracer struct {
	mu    sync.Mutex
	spans []*recordedSpan
}

type recordedSpan struct {
	name     string
	spanType string
	tags     map[string]any
	input    any
	output   any
	err      error
	code     int
	model    string
	inTok    int
	outTok   int
	finished bool
}

type fakeSpan struct {
	ctx context.Context
	r   *recordedSpan
}

func (t *fakeTracer) StartSpan(ctx context.Context, name, spanType string, tags map[string]any, input any) (context.Context, observabilityStub) {
	r := &recordedSpan{name: name, spanType: spanType, tags: tags, input: input}
	t.mu.Lock()
	t.spans = append(t.spans, r)
	t.mu.Unlock()
	return ctx, &fakeSpan{ctx: ctx, r: r}
}
func (t *fakeTracer) Close(context.Context) {}

type observabilityStub = observability.Span

func (s *fakeSpan) SetTags(_ context.Context, tags map[string]any) {
	if s.r.tags == nil {
		s.r.tags = map[string]any{}
	}
	for k, v := range tags {
		s.r.tags[k] = v
	}
}
func (s *fakeSpan) SetInput(_ context.Context, v any)        { s.r.input = v }
func (s *fakeSpan) SetOutput(_ context.Context, v any)       { s.r.output = v }
func (s *fakeSpan) SetError(_ context.Context, e error)      { s.r.err = e }
func (s *fakeSpan) SetStatusCode(_ context.Context, c int)   { s.r.code = c }
func (s *fakeSpan) SetModelName(_ context.Context, m string) { s.r.model = m }
func (s *fakeSpan) SetInputTokens(_ context.Context, n int)  { s.r.inTok = n }
func (s *fakeSpan) SetOutputTokens(_ context.Context, n int) { s.r.outTok = n }
func (s *fakeSpan) Finish(context.Context)                   { s.r.finished = true }

// fakeChatModel is a minimal Eino ChatModel returning canned content.
type fakeChatModel struct {
	content string
	err     error
	usage   *schema.TokenUsage
}

func (f *fakeChatModel) Generate(context.Context, []*schema.Message, ...cmodel.Option) (*schema.Message, error) {
	if f.err != nil {
		return nil, f.err
	}
	msg := schema.AssistantMessage(f.content, nil)
	if f.usage != nil {
		msg.ResponseMeta = &schema.ResponseMeta{Usage: f.usage}
	}
	return msg, nil
}
func (f *fakeChatModel) Stream(context.Context, []*schema.Message, ...cmodel.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("not implemented")
}
func (f *fakeChatModel) BindTools([]*schema.ToolInfo) error { return nil }

// TestRunEmitsWorkflowToolAndModelSpans verifies the fixed workflow opens
// one root span, one span per fixed tool step and one model span carrying
// token usage, and that all spans are finished.
func TestRunEmitsWorkflowToolAndModelSpans(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ready":
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"summary":"redis unavailable"}`))
		case "/api/data":
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":"internal error"}`))
		default:
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer srv.Close()

	cfg := DefaultTarget()
	cfg.BaseURL = srv.URL

	chat := &fakeChatModel{
		content: `{"status":"diagnosed","root_cause":"redis_not_running","confidence":0.9,` +
			`"evidence":[{"source":"check_http_endpoint","fact":"/ready returned 503"}],` +
			`"suggested_actions":["start redis"]}`,
		usage: &schema.TokenUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
	}
	tr := &fakeTracer{}
	report, err := Run(context.Background(), "span-run", cfg, chat, "fake-model", nil, tr)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.DiagnosisSource != "model" {
		t.Fatalf("want model source, got %s", report.DiagnosisSource)
	}

	var roots, tools, models int
	for _, s := range tr.spans {
		if !s.finished {
			t.Errorf("span %s not finished", s.name)
		}
		switch s.spanType {
		case spanTypeWorkflow:
			roots++
		case spanTypeTool:
			tools++
		case spanTypeModel:
			models++
			if s.model != "fake-model" {
				t.Errorf("model name not propagated: %q", s.model)
			}
			if s.inTok != 11 || s.outTok != 7 {
				t.Errorf("token usage not propagated: in=%d out=%d", s.inTok, s.outTok)
			}
			if s.output == nil {
				t.Error("model span missing output")
			}
		}
	}
	if roots != 1 || tools != 6 || models != 1 {
		t.Fatalf("want 1 root / 6 tool / 1 model spans, got roots=%d tools=%d models=%d", roots, tools, models)
	}
}

// TestRunToolSpansMarkFailure ensures a tool envelope failure marks the
// span status non-zero without aborting the fixed sequence.
func TestRunToolSpansMarkFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	cfg := DefaultTarget()
	cfg.BaseURL = srv.URL
	tr := &fakeTracer{}
	if _, err := Run(context.Background(), "span-run2", cfg, nil, "", nil, tr); err != nil {
		t.Fatalf("run: %v", err)
	}
	var failedTools, finishedTools int
	for _, s := range tr.spans {
		if s.spanType != spanTypeTool {
			continue
		}
		finishedTools++
		if s.code != 0 {
			failedTools++
		}
	}
	if finishedTools != 6 {
		t.Fatalf("want 6 tool spans, got %d", finishedTools)
	}
	// docker-backed steps fail in sandbox (query_service_logs + 2 containers);
	// the 3 HTTP steps pass.
	if failedTools != 3 {
		t.Fatalf("want 3 failed (logs + 2 containers) tool spans, got %d", failedTools)
	}
}

var _ = json.Marshal
var _ model.ToolError
