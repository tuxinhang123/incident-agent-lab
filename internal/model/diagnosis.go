// Package model defines shared structures for diagnostic tools and results.
package model

import (
	"encoding/json"
	"time"
)

// Tool invocation error taxonomy. Tools must map every failure to one of
// these coarse types instead of leaking raw error strings to the caller.
const (
	ErrorTypeTimeout           = "timeout"
	ErrorTypeConnectionFailed  = "connection_failed"
	ErrorTypeBadStatus         = "bad_status"
	ErrorTypeContainerMissing  = "container_not_found"
	ErrorTypeDockerUnavailable = "docker_unavailable"
	ErrorTypeInvalidInput      = "invalid_input"
	ErrorTypeInternal          = "internal_error"
)

// ToolError is the structured failure every tool returns on error.
type ToolError struct {
	Type    string `json:"error_type"`
	Message string `json:"message"`
}

func (e *ToolError) Error() string { return e.Type + ": " + e.Message }

// ToolCall is one audited invocation of a diagnostic tool. Calls are
// append-only and never contain raw credentials or full payloads.
type ToolCall struct {
	RunID     string          `json:"run_id"`
	Tool      string          `json:"tool"`
	Args      map[string]any  `json:"args"`
	StartedAt time.Time       `json:"started_at"`
	LatencyMS int64           `json:"latency_ms"`
	OK        bool            `json:"ok"`
	ErrorType string          `json:"error_type,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
}
