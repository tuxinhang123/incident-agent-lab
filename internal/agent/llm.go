package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	openai "github.com/cloudwego/eino-ext/components/model/openai"
	cmodel "github.com/cloudwego/eino/components/model"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

// ModelConfig is the optional LLM backend. The workflow runs without it:
// a missing API key disables the model and the rule-based diagnoser is used.
type ModelConfig struct {
	APIKey   string
	BaseURL  string
	Model    string
	Endpoint string // "chat" vs "responses"; empty lets the SDK default
}

// LoadModelConfig reads the OpenAI-compatible configuration from the
// environment. OPENAI_API_KEY absence means "model disabled".
func LoadModelConfig() *ModelConfig {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil
	}
	return &ModelConfig{
		APIKey:  key,
		BaseURL: os.Getenv("OPENAI_BASE_URL"),
		Model:   envOr("MODEL_NAME", "gpt-4o-mini"),
	}
}

// NewChatModel builds an Eino ChatModel for an OpenAI-compatible endpoint.
func NewChatModel(ctx context.Context, cfg *ModelConfig) (cmodel.ChatModel, error) {
	if cfg == nil || cfg.APIKey == "" {
		return nil, nil
	}
	m, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		// Per-request HTTP deadline, independent of the agent loop's total
		// budget: it applies fresh to every model turn instead of shrinking
		// as cumulative loop time is spent. The SDK otherwise uses
		// http.DefaultClient, which has no timeout at all. Override with
		// MODEL_HTTP_TIMEOUT (e.g. "120s").
		HTTPClient: &http.Client{Timeout: modelHTTPTimeout()},
		// Reasoning models (e.g. doubao-seed pro on Ark) default to "medium"
		// reasoning and can spend 30s+ on hidden chain-of-thought. "low" keeps
		// the structured diagnosis while keeping latency well under the timeout.
		ReasoningEffort: openai.ReasoningEffortLevelLow,
	})
	if err != nil {
		return nil, fmt.Errorf("create chat model: %w", err)
	}
	return m, nil
}

// modelHTTPTimeout bounds a single chat-completion HTTP request. It is a
// wall-clock per-call cap (connect + TLS + body read), reset on every call.
func modelHTTPTimeout() time.Duration {
	if v := os.Getenv("MODEL_HTTP_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 90 * time.Second
}

const agentSystemPrompt = `You are an incident diagnosis agent. You inspect a target service using read-only tools and return a root-cause diagnosis.

Workflow:
1. Call collect_service_evidence to obtain the baseline evidence bundle.
2. Read the bundle and form a hypothesis. If a signal is ambiguous or contradictory, use the finer-grained tools for targeted follow-up (probe an extra URL, re-scan logs, check a container). Do not re-run collect_service_evidence.
3. When you have enough evidence, respond with a single JSON diagnosis object and NO tool calls.

Available tools:
- collect_service_evidence: returns a structured evidence bundle with fields:
  - health, ready, data: HTTP probe results for the liveness/readiness/business endpoints. Each has {source, ok, status_code, error_type, body_summary}. ok=false means the request failed; error_type is one of timeout|connection_refused|bad_status|internal.
  - app_logs: aggregated recent service errors with {source, ok, lines_scanned, errors:[{error_type, count, sample}]}.
  - redis_status, app_status: container state with {source, ok, status, running, exit_code}.
- check_http_endpoint: GET a URL, returns {ok, status_code, latency_ms, body_summary}.
- query_service_logs: scan recent container logs, returns aggregated errors by error_type.
- get_container_status: returns a Docker container status, running flag, exit code.

Interpreting evidence:
- health unreachable / connection_refused: the process itself is likely down.
- ready 503 (service unavailable): a dependency (e.g. Redis) is unavailable.
- data 500 with a data-store error: also points at the Redis dependency.
- A request that times out (error_type=timeout or high latency_ms) while the process and containers are up indicates app_endpoint_timeout, not app_stopped.
- Use container running flags and log error_type counts to confirm or disambiguate.

Diagnosis taxonomy (root_cause must be exactly one of):
- redis_not_running: the Redis dependency is stopped/unreachable.
- app_stopped: the demo-api process/container itself is down.
- app_endpoint_timeout: an endpoint times out while the process is alive.
- no_incident: service is healthy.
- unknown: evidence is insufficient or contradictory.

Rules:
- Every root cause except "unknown" must be backed by tool evidence. Do not invent facts.
- Confidence is between 0 and 1.
- Your final message must be a single JSON object matching exactly:
{"status":"diagnosed|insufficient_evidence|no_incident","root_cause":"<taxonomy value>","confidence":0.0,"evidence":[{"source":"<tool name>","fact":"<short fact>"}],"suggested_actions":["..."]}
- Use status "insufficient_evidence" with root_cause "unknown" when evidence does not support a specific cause.
- Do not include any text outside the JSON object in your final response.`

func parseModelDiagnosis(content string) *model.Diagnosis {
	s := strings.TrimSpace(content)
	if i := strings.Index(s, "{"); i > 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 && j < len(s)-1 {
		s = s[:j+1]
	}
	var d model.Diagnosis
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		return nil
	}
	if d.RootCause == "" {
		return nil
	}
	return &d
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
