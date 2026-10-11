package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

const (
	logsDefaultTail    = 200
	logsMaxTail        = 5000
	logsMaxErrorGroups = 20
	logsSampleMaxChars = 300
	logsCmdTimeout     = 8 * time.Second
)

type ServiceLogsInput struct {
	Service string `json:"service"`
	Tail    int    `json:"tail_lines,omitempty"`
}

type ErrorAggregate struct {
	ErrorType string `json:"error_type"`
	Count     int    `json:"count"`
	Sample    string `json:"sample"`
}

type ServiceLogsResult struct {
	Service       string           `json:"service"`
	LinesScanned  int              `json:"lines_scanned"`
	MatchedErrors []ErrorAggregate `json:"matched_errors"`
}

type logLine struct {
	Level     string `json:"level"`
	Operation string `json:"operation"`
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
}

// QueryServiceLogs reads recent container logs and aggregates structured
// error entries by error_type, keeping one sample message per type. It
// never returns raw log lines to the caller.
func QueryServiceLogs(in ServiceLogsInput) (*ServiceLogsResult, *model.ToolError) {
	return queryServiceLogs(in, defaultCommandRunner())
}

func queryServiceLogs(in ServiceLogsInput, run commandRunner) (*ServiceLogsResult, *model.ToolError) {
	if strings.TrimSpace(in.Service) == "" {
		return nil, &model.ToolError{Type: model.ErrorTypeInvalidInput, Message: "service is required"}
	}
	tail := in.Tail
	if tail <= 0 {
		tail = logsDefaultTail
	}
	if tail > logsMaxTail {
		return nil, &model.ToolError{
			Type:    model.ErrorTypeInvalidInput,
			Message: "tail_lines must be <= 5000",
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), logsCmdTimeout)
	defer cancel()

	out, err := run(ctx, "docker", "logs", "--tail", strconv.Itoa(tail), in.Service)
	if err != nil {
		return nil, classifyLogsError(err)
	}
	return aggregateLogs(in.Service, out), nil
}

func aggregateLogs(service string, raw []byte) *ServiceLogsResult {
	res := &ServiceLogsResult{Service: service, MatchedErrors: []ErrorAggregate{}}
	groups := map[string]*ErrorAggregate{}
	var order []string

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := stripDockerTimestamp(scanner.Bytes())
		var entry logLine
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		res.LinesScanned++
		if entry.Level != "error" {
			continue
		}
		key := entry.ErrorType
		if key == "" {
			key = "unspecified"
		}
		g, ok := groups[key]
		if !ok {
			g = &ErrorAggregate{ErrorType: key}
			groups[key] = g
			order = append(order, key)
		}
		g.Count++
		if g.Sample == "" {
			g.Sample = truncate(entry.Message, logsSampleMaxChars)
		}
	}

	for _, key := range order {
		if len(res.MatchedErrors) >= logsMaxErrorGroups {
			break
		}
		res.MatchedErrors = append(res.MatchedErrors, *groups[key])
	}
	return res
}

// stripDockerTimestamp removes the "2006-01-02T15:04:05.000000000Z " prefix
// that `docker logs --timestamps` would add; harmless for plain JSON lines.
func stripDockerTimestamp(line []byte) []byte {
	// JSON log lines start with '{'; docker timestamps start with a digit.
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return trimmed
	}
	if idx := bytes.IndexByte(trimmed, '{'); idx > 0 {
		return trimmed[idx:]
	}
	return trimmed
}

func classifyLogsError(err error) *model.ToolError {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr := string(exitErr.Stderr)
		if strings.Contains(stderr, "No such object") || strings.Contains(stderr, "No such container") {
			return &model.ToolError{Type: model.ErrorTypeContainerMissing, Message: "container not found"}
		}
		return &model.ToolError{Type: model.ErrorTypeDockerUnavailable, Message: firstLine(stderr)}
	}
	if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "no such file") {
		return &model.ToolError{Type: model.ErrorTypeDockerUnavailable, Message: "docker CLI not found"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &model.ToolError{Type: model.ErrorTypeTimeout, Message: "docker logs timed out"}
	}
	return &model.ToolError{Type: model.ErrorTypeDockerUnavailable, Message: err.Error()}
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
