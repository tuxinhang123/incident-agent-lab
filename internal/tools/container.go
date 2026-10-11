package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

const dockerCmdTimeout = 5 * time.Second

type ContainerStatusInput struct {
	Container string `json:"container"`
}

type ContainerStatusResult struct {
	Status     string    `json:"status"`
	Running    bool      `json:"running"`
	ExitCode   *int      `json:"exit_code,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

type dockerInspectState struct {
	Status     string    `json:"Status"`
	Running    bool      `json:"Running"`
	ExitCode   int       `json:"ExitCode"`
	StartedAt  time.Time `json:"StartedAt"`
	FinishedAt time.Time `json:"FinishedAt"`
}

type dockerInspectEntry struct {
	State dockerInspectState `json:"State"`
}

// commandRunner abstracts command execution so tests can stub docker.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func defaultCommandRunner() commandRunner {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}
}

// GetContainerStatus queries `docker inspect` for a single container.
func GetContainerStatus(in ContainerStatusInput) (*ContainerStatusResult, *model.ToolError) {
	return getContainerStatus(in, defaultCommandRunner())
}

func getContainerStatus(in ContainerStatusInput, run commandRunner) (*ContainerStatusResult, *model.ToolError) {
	if strings.TrimSpace(in.Container) == "" {
		return nil, &model.ToolError{Type: model.ErrorTypeInvalidInput, Message: "container is required"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), dockerCmdTimeout)
	defer cancel()

	out, err := run(ctx, "docker", "inspect", "--format", "{{json .State}}", in.Container)
	if err != nil {
		return classifyDockerError(err, out)
	}

	var state dockerInspectState
	if err := json.Unmarshal(bytesTrimSpace(out), &state); err != nil {
		return nil, &model.ToolError{Type: model.ErrorTypeInternal, Message: "cannot parse docker inspect output: " + err.Error()}
	}

	res := &ContainerStatusResult{
		Status:     state.Status,
		Running:    state.Running,
		StartedAt:  state.StartedAt,
		FinishedAt: state.FinishedAt,
	}
	if !state.Running {
		exit := state.ExitCode
		res.ExitCode = &exit
	}
	return res, nil
}

func classifyDockerError(err error, out []byte) (*ContainerStatusResult, *model.ToolError) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		stderr := string(exitErr.Stderr)
		if strings.Contains(stderr, "No such object") || strings.Contains(stderr, "No such container") {
			return nil, &model.ToolError{Type: model.ErrorTypeContainerMissing, Message: "container not found"}
		}
		return nil, &model.ToolError{Type: model.ErrorTypeDockerUnavailable, Message: firstLine(stderr)}
	}
	if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "no such file") {
		return nil, &model.ToolError{Type: model.ErrorTypeDockerUnavailable, Message: "docker CLI not found"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, &model.ToolError{Type: model.ErrorTypeTimeout, Message: "docker inspect timed out"}
	}
	return nil, &model.ToolError{Type: model.ErrorTypeDockerUnavailable, Message: err.Error()}
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "docker command failed"
	}
	return s
}
