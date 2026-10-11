package tools

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

func TestAggregateLogsGroupsByErrorType(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`{"level":"info","service":"demo-api","operation":"http_request","message":"ok"}`,
		`{"level":"error","service":"demo-api","operation":"redis_get","error_type":"connection_refused","message":"failed to connect to redis"}`,
		`{"level":"error","service":"demo-api","operation":"redis_ping","error_type":"connection_refused","message":"failed to connect to redis"}`,
		`{"level":"error","service":"demo-api","operation":"redis_get","error_type":"timeout","message":"read timed out"}`,
		`not a json line`,
	}, "\n"))

	res, err := aggregateLogs("demo-api", raw)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}
	if res.LinesScanned != 4 {
		t.Fatalf("want 4 json lines scanned, got %d", res.LinesScanned)
	}
	if len(res.MatchedErrors) != 2 {
		t.Fatalf("want 2 error groups, got %+v", res.MatchedErrors)
	}
	first := res.MatchedErrors[0]
	if first.ErrorType != "connection_refused" || first.Count != 2 || first.Sample != "failed to connect to redis" {
		t.Fatalf("unexpected first group: %+v", first)
	}
}

func TestAggregateLogsEmpty(t *testing.T) {
	res, err := aggregateLogs("demo-api", nil)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}
	if res.LinesScanned != 0 || len(res.MatchedErrors) != 0 {
		t.Fatalf("want empty result, got %+v", res)
	}
}

func TestAggregateLogsTooLongLineReturnsInternalError(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"level":"error","message":"`)
	b.WriteString(strings.Repeat("x", 1024*1024+10))
	b.WriteString("\"}\n")
	b.WriteString(`{"level":"error","service":"demo-api","operation":"redis_get","error_type":"timeout","message":"after the long line"}` + "\n")

	res, err := aggregateLogs("demo-api", []byte(b.String()))
	if err == nil || err.Type != model.ErrorTypeInternal {
		t.Fatalf("want internal_error for overlong line, got res=%+v err=%+v", res, err)
	}
}

func TestQueryServiceLogsTooLongLineReturnsInternalError(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"level":"error","message":"`)
	b.WriteString(strings.Repeat("x", 1024*1024+10))
	b.WriteString("\"}\n")
	b.WriteString(`{"level":"error","error_type":"timeout","message":"after the long line"}` + "\n")

	res, err := queryServiceLogs(ServiceLogsInput{Service: "ial-demo-api"}, stubRunner([]byte(b.String()), nil))
	if err == nil || err.Type != model.ErrorTypeInternal {
		t.Fatalf("want internal_error from queryServiceLogs, got res=%+v err=%+v", res, err)
	}
}

func TestQueryServiceLogsRunner(t *testing.T) {
	raw := []byte(`{"level":"error","service":"demo-api","operation":"redis_get","error_type":"connection_refused","message":"failed to connect to redis"}` + "\n")
	res, err := queryServiceLogs(ServiceLogsInput{Service: "ial-demo-api"}, stubRunner(raw, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.MatchedErrors) != 1 || res.MatchedErrors[0].Count != 1 {
		t.Fatalf("unexpected aggregates: %+v", res.MatchedErrors)
	}
}

func TestQueryServiceLogsValidation(t *testing.T) {
	if _, err := queryServiceLogs(ServiceLogsInput{Service: " "}, stubRunner(nil, nil)); err == nil || err.Type != model.ErrorTypeInvalidInput {
		t.Fatalf("want invalid_input, got %+v", err)
	}
	if _, err := queryServiceLogs(ServiceLogsInput{Service: "x", Tail: logsMaxTail + 1}, stubRunner(nil, nil)); err == nil || err.Type != model.ErrorTypeInvalidInput {
		t.Fatalf("want invalid_input for tail, got %+v", err)
	}
}

func TestQueryServiceLogsContainerMissing(t *testing.T) {
	_, err := queryServiceLogs(ServiceLogsInput{Service: "ghost"}, stubRunner(nil, &exec.ExitError{Stderr: []byte("Error: No such container: ghost")}))
	if err == nil || err.Type != model.ErrorTypeContainerMissing {
		t.Fatalf("want container_not_found, got %+v", err)
	}
}

func TestClassifyLogsErrorTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if got := ctx.Err(); got != context.DeadlineExceeded {
		t.Fatalf("test setup: want expired ctx, got %v", got)
	}

	err := classifyLogsError(ctx, &exec.ExitError{Stderr: []byte("killed")})
	if err == nil || err.Type != model.ErrorTypeTimeout {
		t.Fatalf("want timeout when ctx deadline exceeded even with ExitError, got %+v", err)
	}

	aliveCtx, aliveCancel := context.WithTimeout(context.Background(), time.Second)
	defer aliveCancel()
	err = classifyLogsError(aliveCtx, &exec.ExitError{Stderr: []byte("daemon down")})
	if err == nil || err.Type != model.ErrorTypeDockerUnavailable {
		t.Fatalf("want docker_unavailable for ExitError without timeout, got %+v", err)
	}
}

func TestStripDockerTimestamp(t *testing.T) {
	line := []byte("2026-10-11T09:00:00.000000000Z " + `{"level":"error","error_type":"x","message":"m"}`)
	got := stripDockerTimestamp(line)
	if got[0] != '{' {
		t.Fatalf("timestamp not stripped: %q", got)
	}
}
