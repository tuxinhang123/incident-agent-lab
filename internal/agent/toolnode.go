package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// ToolStep is one deterministic call in the fixed workflow: the exact tool
// name (as registered in Eino), its JSON arguments and a delivery callback
// that maps the tool envelope into the evidence bag. Steps run sequentially
// in slice order.
//
// Arguments holds exactly the tool's JSON-schema fields and is what gets
// serialized into the Eino tool call. AuditArgs optionally carries the
// arguments recorded in the audit trail when those include collector-only
// metadata (e.g. a human-readable label) that is not part of the tool
// schema; when nil, the audit trail records Arguments.
type ToolStep struct {
	Name      string
	Arguments map[string]any
	AuditArgs map[string]any
	Deliver   func(env *toolEnvelope, raw json.RawMessage, bag *observationBag)
}

// TraceEntry is one executed workflow step kept in the run report.
type TraceEntry struct {
	Step      int    `json:"step"`
	Tool      string `json:"tool"`
	OK        bool   `json:"ok"`
	ErrorType string `json:"error_type,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
}

// FixedWorkflowToolNode executes a fixed, ordered list of tool calls through
// a real Eino compose.ToolsNode. Tools are never selected by a model here:
// the step list defines the deterministic evidence-collection order from
// the design doc (HTTP endpoints -> logs -> containers). Each step is one
// Assistant message carrying a single tool call, so invocation order equals
// the ToolsNode input order and step results map back 1:1.
type FixedWorkflowToolNode struct {
	node  *compose.ToolsNode
	steps []ToolStep
}

// NewFixedWorkflowToolNode builds the Eino ToolsNode from the registered
// tools and validates that every step names a registered tool.
func NewFixedWorkflowToolNode(ctx context.Context, tools []tool.BaseTool, steps []ToolStep) (*FixedWorkflowToolNode, error) {
	registered := map[string]bool{}
	for _, t := range tools {
		info, err := t.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("read tool info: %w", err)
		}
		registered[info.Name] = true
	}
	for _, s := range steps {
		if !registered[s.Name] {
			return nil, fmt.Errorf("workflow step references unregistered tool %q", s.Name)
		}
	}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: tools})
	if err != nil {
		return nil, fmt.Errorf("create tools node: %w", err)
	}
	return &FixedWorkflowToolNode{node: node, steps: steps}, nil
}

// Run executes every step in fixed order through the Eino ToolsNode and
// returns the populated evidence bag plus a per-step trace. One failed tool
// call would make the node return a framework error; since the registered
// tools envelope their failures ({ok:false,...}) instead of returning Go
// errors, the full step sequence is always executed.
func (w *FixedWorkflowToolNode) Run(ctx context.Context) (*observationBag, []TraceEntry, error) {
	bag := &observationBag{}
	trace := make([]TraceEntry, 0, len(w.steps))
	for i, step := range w.steps {
		args, err := json.Marshal(step.Arguments)
		if err != nil {
			return nil, nil, fmt.Errorf("step %d marshal args: %w", i, err)
		}
		msg := schema.AssistantMessage("", nil)
		msg.ToolCalls = []schema.ToolCall{{
			ID:   fmt.Sprintf("call-%d", i),
			Type: "function",
			Function: schema.FunctionCall{
				Name:      step.Name,
				Arguments: string(args),
			},
		}}
		started := nowFn()
		out, err := w.node.Invoke(ctx, msg)
		latencyMS := timeSinceMS(started)
		if err != nil {
			return nil, nil, fmt.Errorf("step %d (%s): %w", i, step.Name, err)
		}
		if len(out) != 1 {
			return nil, nil, fmt.Errorf("step %d (%s): expected 1 tool message, got %d", i, step.Name, len(out))
		}
		env := &toolEnvelope{}
		if err := json.Unmarshal([]byte(out[0].Content), env); err != nil {
			return nil, nil, fmt.Errorf("step %d (%s) parse result: %w", i, step.Name, err)
		}
		raw, _ := json.Marshal(env.Result)
		if step.Deliver != nil {
			step.Deliver(env, raw, bag)
		}
		trace = append(trace, TraceEntry{
			Step: i + 1, Tool: step.Name, OK: env.OK,
			ErrorType: env.ErrorType, LatencyMS: latencyMS,
		})
	}
	return bag, trace, nil
}
