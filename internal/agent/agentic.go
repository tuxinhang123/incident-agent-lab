package agent

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	emodel "github.com/cloudwego/eino/components/model"
	etool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	diagmodel "github.com/tuxinhang123/incident-agent-lab/internal/model"
	"github.com/tuxinhang123/incident-agent-lab/internal/observability"
)

// maxAgentSteps bounds the ReAct generation cycles so a chatty model cannot
// spin forever. ADK's ChatModelAgent defaults to 20; we tighten it.
const maxAgentSteps = 12

// agentName identifies this agent in multi-agent-ready ADK tracing.
const agentName = "incident-diagnoser"

// agentLoopTimeout caps the whole agent run (all model turns + tool calls).
// It is layered on the caller's deadline (DIAGNOSE_TIMEOUT in the CLI, default
// 180s), so the effective budget is whichever is shorter. The default is kept
// just below the CLI's parent budget as a safety net for non-CLI callers that
// pass an unbounded context. Overridable via AGENT_LOOP_TIMEOUT (MODEL_TIMEOUT
// honored for backwards compatibility).
func agentLoopTimeout() time.Duration {
	for _, key := range []string{"AGENT_LOOP_TIMEOUT", "MODEL_TIMEOUT"} {
		if v := os.Getenv(key); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				return d
			}
		}
	}
	return 150 * time.Second
}

// RunAgenticLoop drives the standard Eino ADK agent: an adk.ChatModelAgent
// executed through adk.Runner (the mandatory entry point). The model decides
// which tool to call (and with what arguments), tool results feed back into
// the next model turn, and the loop ends when the model emits a final
// diagnosis message with no tool calls.
//
// Tools are passed per request via the model.WithTools option (ADK convention);
// the ChatModel itself is not mutated with BindTools. Tracing is bridged to
// Coze Loop via an Eino callback handler attached through adk.WithCallbacks.
func RunAgenticLoop(ctx context.Context, chat emodel.ChatModel, modelName string, cfg TargetConfig, audit AuditSink, tr observability.Tracer, runID string) (*diagmodel.Diagnosis, []TraceEntry, error) {
	registered, err := AgentTools(cfg, audit, runID)
	if err != nil {
		return nil, nil, fmt.Errorf("register tools: %w", err)
	}

	agt, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        agentName,
		Description: "Read-only incident diagnosis agent that inspects a target service and returns a root-cause diagnosis.",
		Instruction: agentSystemPrompt,
		Model:       chat,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: toBaseTools(registered),
			},
		},
		MaxIterations: maxAgentSteps,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create chat model agent: %w", err)
	}

	// Runner is the single entry point for executing an ADK agent.
	// EnableStreaming=false makes every assistant generation arrive as one
	// complete message, which is all we need to extract the final JSON.
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agt})

	userMsg := fmt.Sprintf(
		"Diagnose the health of the service at %s.\n\n"+
			"App container: %s. Redis container: %s.\n"+
			"Call collect_service_evidence first to get the baseline evidence bundle, "+
			"use the other tools only for targeted follow-up, then return your diagnosis as a JSON object.",
		cfg.BaseURL, cfg.AppContainer, cfg.RedisContainer,
	)
	input := []adk.Message{schema.UserMessage(userMsg)}

	// Attach the official Coze Loop callback handler (when tracing is
	// configured). It auto-instruments the ADK Agent span plus every model
	// turn and tool call — names, token usage, arguments and results.
	var runOpts []adk.AgentRunOption
	if hp, ok := tr.(loopHandlerProvider); ok {
		if h := hp.EinoLoopHandler(); h != nil {
			runOpts = append(runOpts, adk.WithCallbacks(h))
		}
	}

	loopCtx, cancel := context.WithTimeout(ctx, agentLoopTimeout())
	defer cancel()

	iter := runner.Run(loopCtx, input, runOpts...)

	// Consume the event stream. Tool-role events populate the execution
	// trace; the final assistant-role message (no tool calls) is the answer.
	var (
		trace    []TraceEntry
		final    *schema.Message
		toolStep int
	)
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return nil, trace, fmt.Errorf("agent run: %w", event.Err)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return nil, trace, fmt.Errorf("read agent message: %w", err)
		}
		if msg == nil {
			continue
		}
		switch msg.Role {
		case schema.Tool:
			toolStep++
			trace = append(trace, TraceEntry{
				Step: toolStep,
				Tool: event.Output.MessageOutput.ToolName,
				OK:   true,
			})
		case schema.Assistant:
			// Each generation is emitted in order; the last assistant
			// message is the one that terminates the loop (no tool calls).
			final = msg
		}
	}

	if final == nil {
		return nil, trace, fmt.Errorf("agent produced no final assistant message")
	}

	d := parseModelDiagnosis(final.Content)
	if d == nil {
		return nil, trace, fmt.Errorf("agent did not return a parseable diagnosis")
	}
	return d, trace, nil
}

// toBaseTools converts registered InvokableTool instances to the BaseTool
// slice form expected by compose.ToolsNodeConfig.
func toBaseTools(tools []etool.InvokableTool) []etool.BaseTool {
	out := make([]etool.BaseTool, len(tools))
	for i, t := range tools {
		out[i] = t
	}
	return out
}

// loopHandlerProvider is an optional capability of a Tracer: it supplies the
// framework-native Eino callback handler (the official eino-ext cozeloop
// integration) that the ADK Runner attaches via adk.WithCallbacks. The agent
// package stays decoupled from the concrete cozeloop dependency.
type loopHandlerProvider interface {
	EinoLoopHandler() callbacks.Handler
}
