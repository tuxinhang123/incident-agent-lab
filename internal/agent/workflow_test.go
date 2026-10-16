package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

// fakeFaultyAPI mimics demo-api during a Redis outage: process is alive
// (/health 200) but readiness and business endpoints fail.
func fakeFaultyAPI() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/ready":
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"status":"unavailable","summary":"redis unavailable"}`))
		case "/api/data":
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"error":"internal error"}`))
		default:
			w.WriteHeader(404)
		}
	}))
}

func fakeHealthyAPI() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
}

// TestRunWorkflowFaultWithoutDocker proves the fixed workflow diagnoses the
// redis outage from HTTP signals even when docker tools are unavailable
// (sandbox/CI without a docker daemon).
func TestRunWorkflowFaultWithoutDocker(t *testing.T) {
	srv := fakeFaultyAPI()
	defer srv.Close()

	cfg := TargetConfig{
		BaseURL:        srv.URL,
		AppContainer:   "ial-demo-api",
		RedisContainer: "ial-redis",
		HTTPTimeoutMS:  2000,
		LogTailLines:   50,
	}
	report, err := Run(context.Background(), "test-run-fault", cfg, nil, "", nil, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if report.DiagnosisSource != "rules" {
		t.Fatalf("without a chat model source must be rules, got %q", report.DiagnosisSource)
	}
	d := report.Diagnosis
	if d.RootCause != model.CauseRedisNotRunning {
		t.Fatalf("want redis_not_running, got %s (evidence: %+v)", d.RootCause, d.Evidence)
	}
	if d.Status != model.StatusDiagnosed || d.Confidence < 0.9 {
		t.Fatalf("unexpected diagnosis shape: %+v", d)
	}
	if report.Evidence.Health == nil || report.Evidence.Health.StatusCode != 200 {
		t.Fatalf("health should still be 200: %+v", report.Evidence.Health)
	}
	if report.Evidence.Ready.StatusCode != 503 || report.Evidence.Data.StatusCode != 500 {
		t.Fatalf("want 503/500, got ready=%+v data=%+v", report.Evidence.Ready, report.Evidence.Data)
	}
}

func TestRunWorkflowHealthy(t *testing.T) {
	srv := fakeHealthyAPI()
	defer srv.Close()

	cfg := TargetConfig{BaseURL: srv.URL, HTTPTimeoutMS: 2000}
	report, err := Run(context.Background(), "test-run-healthy", cfg, nil, "", nil, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if report.Diagnosis.RootCause != model.CauseNone || report.Diagnosis.Status != model.StatusHealthy {
		t.Fatalf("want no_incident, got %+v", report.Diagnosis)
	}
}

func TestEinoToolRegistration(t *testing.T) {
	tools, err := RegisteredTools(nil)
	if err != nil {
		t.Fatalf("register tools: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("want 3 tools, got %d", len(tools))
	}
	want := map[string]bool{
		ToolCheckHTTP: false, ToolServiceLogs: false, ToolContainerGet: false,
	}
	ctx := context.Background()
	for _, tl := range tools {
		info, err := tl.Info(ctx)
		if err != nil {
			t.Fatalf("info: %v", err)
		}
		if _, ok := want[info.Name]; !ok {
			t.Fatalf("unexpected tool %s", info.Name)
		}
		want[info.Name] = true
		if info.ParamsOneOf == nil {
			t.Fatalf("tool %s has no parameter schema", info.Name)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("tool %s not registered", name)
		}
	}
}

func TestRunWorkflowIncludesOrderedTrace(t *testing.T) {
	srv := fakeFaultyAPI()
	defer srv.Close()
	cfg := TargetConfig{BaseURL: srv.URL, AppContainer: "ial-demo-api", RedisContainer: "ial-redis", HTTPTimeoutMS: 2000, LogTailLines: 50}
	report, err := Run(context.Background(), "trace-run", cfg, nil, "", nil, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{ToolCheckHTTP, ToolCheckHTTP, ToolCheckHTTP, ToolServiceLogs, ToolContainerGet, ToolContainerGet}
	if len(report.Trace) != len(want) {
		t.Fatalf("want %d trace entries, got %+v", len(want), report.Trace)
	}
	for i, name := range want {
		if report.Trace[i].Tool != name || report.Trace[i].Step != i+1 {
			t.Fatalf("trace %d: want %s, got %+v", i, name, report.Trace[i])
		}
	}
}
