package agent

import (
	"context"
	"encoding/json"
	"fmt"
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

const diagnoseSystemPrompt = `You are an incident diagnosis agent. You are given structured evidence collected by read-only diagnostic tools.

Decide the root cause using ONLY this taxonomy:
- redis_not_running: the Redis dependency is stopped/unreachable (expect evidence such as redis container exited, connection_refused logs, /ready 503, /api/data 500).
- app_stopped: the demo-api process/container itself is down (/health unreachable and app container not running).
- app_endpoint_timeout: an endpoint times out while the process is alive.
- no_incident: /ready and /api/data both return 200.
- unknown: evidence is insufficient or contradictory.

Rules:
- Every root cause except "unknown" must be backed by evidence taken from the provided observations. Do not invent facts.
- Confidence is between 0 and 1.
- Respond with a single JSON object and nothing else, matching exactly:
{"status":"diagnosed|insufficient_evidence|no_incident","root_cause":"<taxonomy value>","confidence":0.0,"evidence":[{"source":"<tool name>","fact":"<short fact>"}],"suggested_actions":["..."]}
- Use status "insufficient_evidence" with root_cause "unknown" when evidence does not support a specific cause.`

// modelCallTimeout bounds one LLM Generate call.
const modelCallTimeout = 30 * time.Second

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
