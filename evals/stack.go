package evals

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Mode selects how the target stack is realized.
type Mode string

const (
	ModeAuto   Mode = "auto"
	ModeDocker Mode = "docker"
	ModeLocal  Mode = "local"
)

const (
	seedKey   = "demo:greeting"
	seedValue = "hello from incident-agent-lab"

	appContainerName   = "ial-demo-api"
	redisContainerName = "ial-redis"
	seedContainerName  = "ial-seed"
)

// Harness owns the target stack and the case lifecycle.
type Harness struct {
	root   string // repository root
	runDir string // .run/evals
	mode   Mode

	projectName string
	composeFile string
	baseURL     string
	httpPort    int
	redisPort   int // local mode only

	// Per-case state, reset before each case runs.
	targetOverride string
	fault          FaultTarget
}

func NewHarness(mode Mode) (*Harness, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	runDir := filepath.Join(root, ".run", "evals")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, fmt.Errorf("create run dir: %w", err)
	}
	h := &Harness{
		root:        root,
		runDir:      runDir,
		mode:        mode,
		projectName: "ial-eval-" + randomSuffix(),
		composeFile: filepath.Join(runDir, "compose.eval.yaml"),
	}
	return h, nil
}

func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return wd, nil
		}
		dir = parent
	}
}

func randomSuffix() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ------------------------------------------------------------------ commands

// execSpec is one captured external command invocation.
type execSpec struct {
	name string
	args []string
	dir  string
	env  []string
}

func (h *Harness) run(ctx context.Context, s execSpec) (string, error) {
	cmd := exec.CommandContext(ctx, s.name, s.args...)
	if s.dir != "" {
		cmd.Dir = s.dir
	} else {
		cmd.Dir = h.root
	}
	cmd.Env = s.env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func (h *Harness) runQuiet(ctx context.Context, s execSpec) error {
	_, err := h.run(ctx, s)
	return err
}

func (h *Harness) compose(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"compose", "-p", h.projectName, "-f", h.composeFile}, args...)
	return h.run(ctx, execSpec{name: "docker", args: full})
}

func haveBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// ----------------------------------------------------------- dependency gate

// Gate verifies the selected mode is usable and resolves "auto".
func (h *Harness) Gate(ctx context.Context) error {
	if !haveBinary("go") {
		return fmt.Errorf("go toolchain not found in PATH")
	}
	if h.mode == ModeAuto {
		if h.dockerDaemonUp(ctx) {
			h.mode = ModeDocker
		} else {
			h.mode = ModeLocal
		}
	}
	switch h.mode {
	case ModeDocker:
		if !haveBinary("docker") || !h.dockerDaemonUp(ctx) {
			return fmt.Errorf("docker mode selected but docker daemon is not available")
		}
	case ModeLocal:
		if !haveBinary("redis-server") {
			return fmt.Errorf("local mode requires redis-server in PATH")
		}
		if !haveBinary("redis-cli") {
			return fmt.Errorf("local mode requires redis-cli in PATH")
		}
	default:
		return fmt.Errorf("unknown mode %q (want auto|docker|local)", h.mode)
	}
	return nil
}

func (h *Harness) dockerDaemonUp(ctx context.Context) bool {
	if !haveBinary("docker") {
		return false
	}
	return h.runQuiet(ctx, execSpec{name: "docker", args: []string{"info"}}) == nil
}

// Mode reports the resolved mode after Gate.
func (h *Harness) Mode() Mode { return h.mode }

// --------------------------------------------------------------- HTTP waits

func httpCode(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

// waitForCode polls url until it returns want (or the deadline passes).
func (h *Harness) waitForCode(ctx context.Context, url string, want int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		code, err := httpCode(probeCtx, url)
		cancel()
		if err == nil && code == want {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for %s to return %d", url, want)
}

// waitForDown polls url until the connection fails (refused/timeout).
func (h *Harness) waitForDown(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := httpCode(probeCtx, url)
		cancel()
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for %s to go down", url)
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// -------------------------------------------------------------- stack bring-up

const evalComposeTemplate = `name: %s
services:
  redis:
    image: redis:7-alpine
    container_name: %s
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 2s
      retries: 5
  demo-api:
    build:
      context: %s
      dockerfile: %s/Dockerfile
    image: ial-eval-demo-api:latest
    container_name: %s
    environment:
      HTTP_ADDR: "0.0.0.0:8080"
      REDIS_ADDR: "redis:6379"
    ports:
      - "127.0.0.1:%d:8080"
    depends_on:
      redis:
        condition: service_healthy
  seed:
    image: redis:7-alpine
    container_name: %s
    depends_on:
      redis:
        condition: service_healthy
    entrypoint:
      - redis-cli
      - -h
      - redis
      - SET
      - %s
      - "%s"
    restart: "no"
`

// Up prepares the target stack and waits for /health=200.
func (h *Harness) Up(ctx context.Context) error {
	if h.mode == ModeDocker {
		return h.upDocker(ctx)
	}
	return h.upLocal(ctx)
}

func (h *Harness) upDocker(ctx context.Context) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	h.httpPort = port
	h.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	content := fmt.Sprintf(evalComposeTemplate,
		h.projectName, redisContainerName,
		h.root, h.root, appContainerName, port, seedContainerName,
		seedKey, seedValue,
	)
	if err := os.WriteFile(h.composeFile, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write eval compose file: %w", err)
	}

	// Remove canonical-name containers left by the deploy/ stack so the
	// isolated eval project can claim the names the agent looks up.
	_, _ = h.run(ctx, execSpec{name: "docker", args: []string{
		"rm", "-f", redisContainerName, appContainerName, seedContainerName,
	}})
	_, _ = h.compose(ctx, "down", "--remove-orphans")

	logf("eval compose project=%s host port=%d", h.projectName, port)
	if out, err := h.compose(ctx, "up", "-d", "--build"); err != nil {
		_ = os.WriteFile(filepath.Join(h.runDir, "compose-up.log"), []byte(out), 0o644)
		return fmt.Errorf("docker compose up failed: %w\n%s", err, tailLines(out, 40))
	}
	if err := h.waitForCode(ctx, h.baseURL+"/health", http.StatusOK, 120*time.Second); err != nil {
		return fmt.Errorf("%w\nstack is running inside compose but host port forwarding does not deliver "+
			"traffic; restart Docker Desktop or rerun with --mode local", err)
	}
	return nil
}

func (h *Harness) upLocal(ctx context.Context) error {
	httpPort, err := freePort()
	if err != nil {
		return err
	}
	redisPort, err := freePort()
	if err != nil {
		return err
	}
	h.httpPort = httpPort
	h.redisPort = redisPort
	h.baseURL = fmt.Sprintf("http://127.0.0.1:%d", httpPort)

	_ = h.runQuiet(ctx, execSpec{name: "bash", args: []string{filepath.Join(h.root, "scripts", "local-down.sh")}})
	s := execSpec{
		name: "bash",
		args: []string{filepath.Join(h.root, "scripts", "local-up.sh")},
		env:  append(os.Environ(), "HTTP_PORT="+strconv.Itoa(httpPort), "REDIS_PORT="+strconv.Itoa(redisPort)),
	}
	if out, err := h.run(ctx, s); err != nil {
		_ = os.WriteFile(filepath.Join(h.runDir, "local-up.log"), []byte(out), 0o644)
		return fmt.Errorf("local stack startup failed: %w\n%s", err, tailLines(out, 40))
	}
	return h.waitForCode(ctx, h.baseURL+"/health", http.StatusOK, 60*time.Second)
}

// Down tears the target stack down.
func (h *Harness) Down(ctx context.Context) {
	if h.mode == ModeDocker {
		_, _ = h.compose(ctx, "down", "--remove-orphans")
		_, _ = h.run(ctx, execSpec{name: "docker", args: []string{
			"rm", "-f", redisContainerName, appContainerName, seedContainerName,
		}})
	} else {
		_ = h.runQuiet(ctx, execSpec{name: "bash", args: []string{filepath.Join(h.root, "scripts", "local-down.sh")}})
	}
}

func (h *Harness) BaseURL() string { return h.baseURL }

// ------------------------------------------------------ redis / app fault ops

// StopRedis stops only redis, keeping demo-api alive.
func (h *Harness) StopRedis(ctx context.Context) error {
	if h.mode == ModeDocker {
		_, err := h.compose(ctx, "stop", "redis")
		return err
	}
	pid, err := readPid(filepath.Join(h.root, ".run", "redis.pid"))
	if err != nil {
		return err
	}
	return stopProcess(pid)
}

// StartRedis restarts redis and re-seeds the demo key.
func (h *Harness) StartRedis(ctx context.Context) error {
	if h.mode == ModeDocker {
		if _, err := h.compose(ctx, "start", "redis"); err != nil {
			return err
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			out, err := h.compose(ctx, "exec", "-T", "redis", "redis-cli", "ping")
			if err == nil && strings.TrimSpace(out) == "PONG" {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		_, err := h.compose(ctx, "exec", "-T", "redis", "redis-cli", "SET", seedKey, seedValue)
		return err
	}
	logFile := filepath.Join(h.root, ".run", "redis.log")
	pidFile := filepath.Join(h.root, ".run", "redis.pid")
	s := execSpec{
		name: "redis-server",
		args: []string{
			"--daemonize", "yes", "--port", strconv.Itoa(h.redisPort),
			"--dir", filepath.Join(h.root, ".run"),
			"--logfile", logFile, "--pidfile", pidFile,
			"--save", "", "--appendonly", "no",
		},
	}
	if _, err := h.run(ctx, s); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, err := h.run(ctx, execSpec{name: "redis-cli", args: []string{"-p", strconv.Itoa(h.redisPort), "ping"}})
		if err == nil && strings.TrimSpace(out) == "PONG" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, err := h.run(ctx, execSpec{name: "redis-cli", args: []string{"-p", strconv.Itoa(h.redisPort), "SET", seedKey, seedValue}})
	return err
}

// StopApp stops demo-api (docker only).
func (h *Harness) StopApp(ctx context.Context) error {
	if h.mode != ModeDocker {
		return fmt.Errorf("StopApp is only supported in docker mode")
	}
	_, err := h.compose(ctx, "stop", "demo-api")
	return err
}

// StartApp restarts demo-api (docker only).
func (h *Harness) StartApp(ctx context.Context) error {
	if h.mode != ModeDocker {
		return fmt.Errorf("StartApp is only supported in docker mode")
	}
	_, err := h.compose(ctx, "start", "demo-api")
	return err
}

// ------------------------------------------------------------ fault targets

// UseBlackhole points the agent at a TCP target that never responds.
func (h *Harness) UseBlackhole() error {
	t, err := startBlackhole()
	if err != nil {
		return err
	}
	h.fault = t
	h.targetOverride = t.URL()
	return nil
}

// UseStatusStub points the agent at a fixed-status-code target.
func (h *Harness) UseStatusStub(code int) error {
	t, err := startStatusStub(code)
	if err != nil {
		return err
	}
	h.fault = t
	h.targetOverride = t.URL()
	return nil
}

// ClearFault closes any synthetic target and resets the agent target.
func (h *Harness) ClearFault() {
	if h.fault != nil {
		_ = h.fault.Close()
		h.fault = nil
	}
	h.targetOverride = ""
}

func (h *Harness) targetURL() string {
	if h.targetOverride != "" {
		return h.targetOverride
	}
	return h.baseURL
}

// ------------------------------------------------------------------- helpers

func readPid(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read pidfile %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("invalid pidfile %s: %w", path, err)
	}
	return pid, nil
}

func stopProcess(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return nil // gone
		}
		time.Sleep(100 * time.Millisecond)
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
