package tools

import (
	"context"
	"os/exec"
	"testing"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

func TestGetContainerStatusExited(t *testing.T) {
	stateJSON := []byte(`{"Status":"exited","Running":false,"ExitCode":0,"StartedAt":"2026-10-11T08:00:00Z","FinishedAt":"2026-10-11T09:00:00Z"}`)
	run := stubRunner(stateJSON, nil)

	res, err := getContainerStatus(ContainerStatusInput{Container: "ial-redis"}, run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Status != "exited" || res.Running {
		t.Fatalf("want exited/not running, got %+v", res)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("want exit_code 0, got %v", res.ExitCode)
	}
}

func TestGetContainerStatusRunningOmitsExitCode(t *testing.T) {
	stateJSON := []byte(`{"Status":"running","Running":true,"ExitCode":0,"StartedAt":"2026-10-11T08:00:00Z","FinishedAt":"0001-01-01T00:00:00Z"}`)
	res, err := getContainerStatus(ContainerStatusInput{Container: "ial-redis"}, stubRunner(stateJSON, nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Running || res.ExitCode != nil {
		t.Fatalf("want running without exit code, got %+v", res)
	}
}

func TestGetContainerStatusMissing(t *testing.T) {
	run := stubRunner(nil, &exec.ExitError{Stderr: []byte("Error: No such object: ial-redis")})
	_, err := getContainerStatus(ContainerStatusInput{Container: "ghost"}, run)
	if err == nil || err.Type != model.ErrorTypeContainerMissing {
		t.Fatalf("want container_not_found, got %+v", err)
	}
}

func TestGetContainerStatusInvalidInput(t *testing.T) {
	_, err := getContainerStatus(ContainerStatusInput{Container: "  "}, stubRunner(nil, nil))
	if err == nil || err.Type != model.ErrorTypeInvalidInput {
		t.Fatalf("want invalid_input, got %+v", err)
	}
}

func TestGetContainerStatusDockerMissing(t *testing.T) {
	run := stubRunner(nil, exec.ErrNotFound)
	_, err := getContainerStatus(ContainerStatusInput{Container: "x"}, run)
	if err == nil || err.Type != model.ErrorTypeDockerUnavailable {
		t.Fatalf("want docker_unavailable, got %+v", err)
	}
}

// When the context deadline expires, exec.CommandContext kills the child
// process and Output returns *exec.ExitError; that must be reported as a
// timeout rather than docker_unavailable.
func TestGetContainerStatusTimeoutKilledReturnsTimeout(t *testing.T) {
	run := func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, &exec.ExitError{Stderr: []byte("")}
	}
	_, err := getContainerStatus(ContainerStatusInput{Container: "slow"}, run)
	if err == nil || err.Type != model.ErrorTypeTimeout {
		t.Fatalf("want timeout, got %+v", err)
	}
}

// A pre-cancelled context combined with an ExitError must also classify as
// timeout, even when stderr happens to contain other text.
func TestClassifyDockerErrorDeadlineExceededBeforeExitError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-ctx.Done()

	_, err := classifyDockerError(ctx, &exec.ExitError{Stderr: []byte("signal: killed")}, nil)
	if err == nil || err.Type != model.ErrorTypeTimeout {
		t.Fatalf("want timeout, got %+v", err)
	}
}

func stubRunner(out []byte, err error) commandRunner {
	return func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return out, err
	}
}
