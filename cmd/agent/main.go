// Command agent exposes the V0 read-only diagnostic tools as a CLI.
// Each subcommand calls exactly one tool, prints its structured JSON result,
// and appends an audit record to .run/tool-calls.jsonl.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/tuxinhang123/incident-agent-lab/internal/agent"
	"github.com/tuxinhang123/incident-agent-lab/internal/model"
	"github.com/tuxinhang123/incident-agent-lab/internal/tools"
)

const auditPath = ".run/tool-calls.jsonl"

// errToolFailed marks a tool-level business failure whose structured
// {ok:false} JSON was already written to stdout; the process must still
// exit non-zero, without duplicating the error on stderr.
var errToolFailed = errors.New("tool failed")

// errUsage marks a flag/usage error. The flag package already printed the
// error and usage to stderr, so main only sets exit code 2.
var errUsage = errors.New("invalid usage")

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	runID := agent.NewRunID()
	audit := agent.NewAuditLog(auditPath)

	var err error
	switch os.Args[1] {
	case "check-http":
		err = runCheckHTTP(os.Args[2:], runID, audit)
	case "container-status":
		err = runContainerStatus(os.Args[2:], runID, audit)
	case "service-logs":
		err = runServiceLogs(os.Args[2:], runID, audit)
	case "diagnose":
		err = runDiagnose(os.Args[2:], runID, audit)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	switch {
	case err == nil:
		return
	case errors.Is(err, errToolFailed):
		// Structured {ok:false} JSON already emitted on stdout; do not
		// duplicate the error on stderr.
		os.Exit(1)
	case errors.Is(err, errUsage):
		// flag.Parse already printed the error and usage to stderr.
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func runCheckHTTP(argv []string, runID string, audit *agent.AuditLog) error {
	fs := flag.NewFlagSet("check-http", flag.ContinueOnError)
	url := fs.String("url", "", "endpoint URL to GET (required)")
	timeoutMS := fs.Int("timeout-ms", 3000, "request timeout in milliseconds (max 10000)")
	if err := fs.Parse(argv); err != nil {
		return errUsage
	}
	in := tools.HTTPEndpointInput{URL: *url, TimeoutMS: *timeoutMS}
	started := time.Now()
	res, toolErr := tools.CheckHTTPEndpoint(in)
	writeAudit(audit, runID, "check_http_endpoint", map[string]any{"url": *url}, started, res, toolErr)
	return emit(res, toolErr)
}

func runContainerStatus(argv []string, runID string, audit *agent.AuditLog) error {
	fs := flag.NewFlagSet("container-status", flag.ContinueOnError)
	name := fs.String("container", "", "docker container name (required)")
	if err := fs.Parse(argv); err != nil {
		return errUsage
	}
	in := tools.ContainerStatusInput{Container: *name}
	started := time.Now()
	res, toolErr := tools.GetContainerStatus(in)
	writeAudit(audit, runID, "get_container_status", map[string]any{"container": *name}, started, res, toolErr)
	return emit(res, toolErr)
}

func runServiceLogs(argv []string, runID string, audit *agent.AuditLog) error {
	fs := flag.NewFlagSet("service-logs", flag.ContinueOnError)
	service := fs.String("service", "", "container/service name (required)")
	tail := fs.Int("tail", 200, "number of recent log lines (max 5000)")
	if err := fs.Parse(argv); err != nil {
		return errUsage
	}
	in := tools.ServiceLogsInput{Service: *service, Tail: *tail}
	started := time.Now()
	res, toolErr := tools.QueryServiceLogs(in)
	writeAudit(audit, runID, "query_service_logs", map[string]any{"service": *service, "tail": *tail}, started, res, toolErr)
	return emit(res, toolErr)
}

func writeAudit(audit *agent.AuditLog, runID, tool string, args map[string]any, started time.Time, result any, toolErr *model.ToolError) {
	if err := audit.Record(runID, tool, args, started, result, toolErr); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write audit log: %v\n", err)
	}
}

func runDiagnose(argv []string, runID string, audit *agent.AuditLog) error {
	def := agent.DefaultTarget()
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	baseURL := fs.String("base-url", def.BaseURL, "base URL of demo-api")
	appContainer := fs.String("app-container", def.AppContainer, "demo-api container name")
	redisContainer := fs.String("redis-container", def.RedisContainer, "redis container name")
	timeoutMS := fs.Int("timeout-ms", def.HTTPTimeoutMS, "per-request timeout in milliseconds")
	tail := fs.Int("tail", def.LogTailLines, "number of recent log lines")
	if err := fs.Parse(argv); err != nil {
		return errUsage
	}

	cfg := agent.TargetConfig{
		BaseURL:        *baseURL,
		AppContainer:   *appContainer,
		RedisContainer: *redisContainer,
		HTTPTimeoutMS:  *timeoutMS,
		LogTailLines:   *tail,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	chat, mErr := agent.NewChatModel(ctx, agent.LoadModelConfig())
	if mErr != nil {
		fmt.Fprintf(os.Stderr, "warning: model disabled: %v\n", mErr)
	}

	report := agent.Run(ctx, runID, cfg, chat, audit)
	return agent.EncodeJSON(os.Stdout, report)
}

func emit(result any, toolErr *model.ToolError) error {
	if toolErr != nil {
		if err := agent.EncodeJSON(os.Stdout, map[string]any{
			"ok":         false,
			"error_type": toolErr.Type,
			"message":    toolErr.Message,
		}); err != nil {
			return err
		}
		return errToolFailed
	}
	return agent.EncodeJSON(os.Stdout, map[string]any{"ok": true, "result": result})
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: agent <subcommand> [flags]

read-only diagnostic tools:
  check-http        -url <url> [-timeout-ms 3000]
  container-status  -container <name>
  service-logs      -service <name> [-tail 200]

fixed-order diagnosis workflow:
  diagnose [-base-url http://127.0.0.1:8080] [-app-container ial-demo-api]
           [-redis-container ial-redis] [-timeout-ms 3000] [-tail 200]
  (set OPENAI_API_KEY / OPENAI_BASE_URL / MODEL_NAME to enable the LLM;
   without a key the built-in rule diagnoser is used)

audit trail is appended to .run/tool-calls.jsonl
`)
}
