// Package agent wires diagnostic tools into an audited CLI workflow.
package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

// NewRunID returns a short random run identifier.
func NewRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:4])
}

// AuditLog is an append-only JSONL record of every tool invocation.
type AuditLog struct {
	mu   sync.Mutex
	path string
}

func NewAuditLog(path string) *AuditLog {
	return &AuditLog{path: path}
}

// Record appends one tool call. Failures to write the audit trail are
// reported but do not hide the tool's own result.
func (l *AuditLog) Record(runID, tool string, args map[string]any, started time.Time, result any, toolErr *model.ToolError) error {
	if l == nil || l.path == "" {
		return nil
	}
	call := model.ToolCall{
		RunID:     runID,
		Tool:      tool,
		Args:      redactArgs(args),
		StartedAt: started.UTC(),
		LatencyMS: time.Since(started).Milliseconds(),
		OK:        toolErr == nil,
	}
	if toolErr != nil {
		call.ErrorType = toolErr.Type
	}
	if result != nil {
		raw, err := json.Marshal(redactResult(result))
		if err == nil {
			call.Result = raw
		}
	}
	line, err := json.Marshal(call)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// EncodeJSON writes v as pretty JSON to w.
func EncodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
