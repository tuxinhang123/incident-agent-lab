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
// redacted by the audit layer before persistence.
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

// Diagnosis status taxonomy.
const (
	StatusDiagnosed            = "diagnosed"
	StatusInsufficientEvidence = "insufficient_evidence"
	StatusHealthy              = "no_incident"
)

// Root cause taxonomy.
const (
	CauseRedisNotRunning = "redis_not_running"
	CauseAppStopped      = "app_stopped"
	CauseAppTimeout      = "app_endpoint_timeout"
	CauseNone            = "no_incident"
	CauseUnknown         = "unknown"
)

// EvidenceItem is one tool-backed fact supporting the root cause.
type EvidenceItem struct {
	Source string `json:"source"`
	Fact   string `json:"fact"`
}

// Diagnosis is the structured output produced after the fixed evidence
// collection workflow.
type Diagnosis struct {
	Status           string         `json:"status"`
	RootCause        string         `json:"root_cause"`
	Confidence       float64        `json:"confidence"`
	Evidence         []EvidenceItem `json:"evidence"`
	SuggestedActions []string       `json:"suggested_actions"`
}

// CollectedEvidence holds raw tool outputs gathered in the fixed workflow.
type CollectedEvidence struct {
	Ready       *HTTPCheckObservation `json:"ready,omitempty"`
	Data        *HTTPCheckObservation `json:"data,omitempty"`
	Health      *HTTPCheckObservation `json:"health,omitempty"`
	AppLogs     *LogObservation       `json:"app_logs,omitempty"`
	RedisStatus *ContainerObservation `json:"redis_status,omitempty"`
	AppStatus   *ContainerObservation `json:"app_status,omitempty"`
}

// HTTPCheckObservation wraps an HTTP tool call result or failure.
type HTTPCheckObservation struct {
	Source     string `json:"source"`
	OK         bool   `json:"ok"`
	StatusCode int    `json:"status_code,omitempty"`
	ErrorType  string `json:"error_type,omitempty"`
	Summary    string `json:"body_summary,omitempty"`
}

// LogObservation wraps the aggregated service log result.
type LogObservation struct {
	Source       string           `json:"source"`
	OK           bool             `json:"ok"`
	ErrorType    string           `json:"error_type,omitempty"`
	LinesScanned int              `json:"lines_scanned,omitempty"`
	Errors       []ErrorAggregate `json:"errors,omitempty"`
}

// ErrorAggregate mirrors the tools layer aggregate without an import cycle.
type ErrorAggregate struct {
	ErrorType string `json:"error_type"`
	Count     int    `json:"count"`
	Sample    string `json:"sample"`
}

// ContainerObservation wraps the container status result.
type ContainerObservation struct {
	Source    string `json:"source"`
	OK        bool   `json:"ok"`
	ErrorType string `json:"error_type,omitempty"`
	Status    string `json:"status,omitempty"`
	Running   bool   `json:"running"`
	ExitCode  *int   `json:"exit_code,omitempty"`
}
