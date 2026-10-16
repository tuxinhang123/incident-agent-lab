package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

// TestFixedWorkflowToolNodeOrderAndEnvelopes runs the real Eino
// compose.ToolsNode with the registered tools and asserts that steps
// execute in the fixed design-doc order and docker-tool failures arrive
// as envelopes (so the full sequence completes in a no-docker sandbox).
func TestFixedWorkflowToolNodeOrderAndEnvelopes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/ready" {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"summary":"redis unavailable"}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	registered, err := RegisteredTools(nil)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	base := make([]tool.BaseTool, len(registered))
	for i, tl := range registered {
		base[i] = tl
	}

	var order []string
	steps := []ToolStep{
		{Name: ToolCheckHTTP, Arguments: map[string]any{"url": srv.URL + "/ready"},
			Deliver: func(_ *toolEnvelope, _ json.RawMessage, _ *observationBag) {
				order = append(order, ToolCheckHTTP)
			}},
		{Name: ToolServiceLogs, Arguments: map[string]any{"service": "ghost"},
			Deliver: func(env *toolEnvelope, _ json.RawMessage, _ *observationBag) {
				order = append(order, ToolServiceLogs)
				if env.OK {
					t.Error("missing container must be an {ok:false} envelope, not a Go error")
				}
				if env.ErrorType == "" {
					t.Error("failure envelope must carry error_type")
				}
			}},
		{Name: ToolContainerGet, Arguments: map[string]any{"container": "ghost"},
			Deliver: func(env *toolEnvelope, _ json.RawMessage, _ *observationBag) {
				order = append(order, ToolContainerGet)
				if env.OK {
					t.Error("missing container must be an {ok:false} envelope")
				}
			}},
	}

	node, err := NewFixedWorkflowToolNode(context.Background(), base, steps)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	_, trace, err := node.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(trace) != 3 {
		t.Fatalf("want 3 trace entries, got %+v", trace)
	}
	wantOrder := []string{ToolCheckHTTP, ToolServiceLogs, ToolContainerGet}
	for i, name := range wantOrder {
		if order[i] != name {
			t.Fatalf("step %d: want %s, got %s (order=%v)", i, name, order[i], order)
		}
		if trace[i].Tool != name || trace[i].Step != i+1 {
			t.Fatalf("trace %d mismatch: %+v", i, trace[i])
		}
	}
	if trace[0].OK != true {
		t.Error("5xx HTTP is an OK envelope (result, not tool failure)")
	}
	if trace[1].OK || trace[2].OK {
		t.Error("docker tool failures must be recorded as not-OK trace entries")
	}
}

// TestNewFixedWorkflowToolNodeRejectsUnknownStep ensures a workflow step
// cannot reference a tool that was not registered.
func TestNewFixedWorkflowToolNodeRejectsUnknownStep(t *testing.T) {
	registered, err := RegisteredTools(nil)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	base := make([]tool.BaseTool, len(registered))
	for i, tl := range registered {
		base[i] = tl
	}
	steps := []ToolStep{{Name: "shell_exec", Arguments: map[string]any{}}}
	if _, err := NewFixedWorkflowToolNode(context.Background(), base, steps); err == nil {
		t.Fatal("unknown tool step must be rejected")
	}
}
