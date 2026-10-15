package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Verdict classifies one case outcome.
type Verdict string

const (
	VerdictPass  Verdict = "PASS"
	VerdictFail  Verdict = "FAIL"
	VerdictSkip  Verdict = "SKIP"
	VerdictError Verdict = "ERROR"
)

const expectedSource = "rules"

// Options configures a suite run.
type Options struct {
	Mode      Mode
	OnlyCase  string
	KeepStack bool
}

// Totals counts verdicts across the suite.
type Totals struct {
	Pass  int `json:"pass"`
	Fail  int `json:"fail"`
	Skip  int `json:"skip"`
	Error int `json:"error"`
}

// CaseResult is one evaluated case in the suite report.
type CaseResult struct {
	Index      int      `json:"index"`
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Question   string   `json:"question"`
	Modes      []string `json:"supported_modes"`
	Verdict    Verdict  `json:"verdict"`
	DurationMS int64    `json:"duration_ms"`
	Expected   Expected `json:"expected"`
	Actual     Actual   `json:"actual"`
	Reason     string   `json:"reason"`
	Report     string   `json:"report"`
}

// Expected is the asserted diagnosis triple.
type Expected struct {
	Status          string `json:"status"`
	RootCause       string `json:"root_cause"`
	DiagnosisSource string `json:"diagnosis_source"`
}

// Actual is what the agent report contained.
type Actual struct {
	Status          string `json:"status"`
	RootCause       string `json:"root_cause"`
	DiagnosisSource string `json:"diagnosis_source"`
}

// SuiteReport is the full eval result document.
type SuiteReport struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Mode        string       `json:"mode"`
	BaseURL     string       `json:"base_url"`
	Diagnoser   string       `json:"diagnoser"`
	Totals      Totals       `json:"totals"`
	Cases       []CaseResult `json:"cases"`
}

// agentReport is the subset of bin/agent's Report the suite asserts on.
type agentReport struct {
	DiagnosisSource string `json:"diagnosis_source"`
	Diagnosis       struct {
		Status    string `json:"status"`
		RootCause string `json:"root_cause"`
	} `json:"diagnosis"`
}

func logf(format string, args ...any) {
	fmt.Printf("[eval] "+format+"\n", args...)
}

// Run executes the full suite and returns the report plus a non-nil error
// when any case FAILs or ERRORs.
func Run(ctx context.Context, opts Options) (*SuiteReport, error) {
	h, err := NewHarness(opts.Mode)
	if err != nil {
		return nil, err
	}
	if err := h.Gate(ctx); err != nil {
		return nil, err
	}
	logf("eval mode: %s", h.Mode())
	h.cleanArtifacts()

	logf("building bin/agent and bin/demo-api ...")
	if err := h.buildBinaries(ctx); err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}

	logf("preparing %s environment ...", h.Mode())
	if err := h.Up(ctx); err != nil {
		return nil, err
	}
	if opts.KeepStack {
		logf("--keep set, leaving %s stack running after the run", h.Mode())
	} else {
		defer h.Down(context.Background())
	}
	logf("stack ready: %s", h.BaseURL())

	all := Cases()
	results := make([]CaseResult, 0, len(all))
	for i, c := range all {
		if opts.OnlyCase != "" && c.ID != opts.OnlyCase {
			continue
		}
		r := h.runCase(ctx, i+1, len(all), c)
		results = append(results, r)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no case matched --case %q", opts.OnlyCase)
	}

	suite, reportPath := h.writeReport(results)

	fmt.Println()
	logf("--- eval summary (mode=%s) ---", h.Mode())
	fmt.Printf("  PASS=%d  FAIL=%d  SKIP=%d  ERROR=%d\n", suite.Totals.Pass, suite.Totals.Fail, suite.Totals.Skip, suite.Totals.Error)
	rel, _ := filepath.Rel(h.root, reportPath)
	fmt.Printf("  report: %s\n", rel)

	if suite.Totals.Fail+suite.Totals.Error > 0 {
		return suite, fmt.Errorf("%d case(s) failed or errored", suite.Totals.Fail+suite.Totals.Error)
	}
	return suite, nil
}

func (h *Harness) buildBinaries(ctx context.Context) error {
	for _, target := range [][2]string{
		{"./cmd/demo-api", filepath.Join("bin", "demo-api")},
		{"./cmd/agent", filepath.Join("bin", "agent")},
	} {
		cmd := exec.CommandContext(ctx, "go", "build", "-o", target[1], target[0])
		cmd.Dir = h.root
		var errBuf bytes.Buffer
		cmd.Stderr = &errBuf
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go build %s: %w\n%s", target[0], err, errBuf.String())
		}
	}
	return nil
}

func (h *Harness) cleanArtifacts() {
	patterns := []string{"*-report.json", "case-*.json", "*-agent.stderr", "results*.json"}
	for _, p := range patterns {
		matches, _ := filepath.Glob(filepath.Join(h.runDir, p))
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
}

func modeNames(ms []Mode) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, string(m))
	}
	return out
}

func (h *Harness) runCase(ctx context.Context, idx, total int, c Case) CaseResult {
	fmt.Println()
	logf("[%d/%d] case: %s — %s", idx, total, c.ID, c.Title)

	res := CaseResult{
		Index: c.Index, ID: c.ID, Title: c.Title, Question: c.Question,
		Modes: modeNames(c.Modes),
		Expected: Expected{
			Status: c.ExpectedStatus, RootCause: c.ExpectedRootCause,
			DiagnosisSource: expectedSource,
		},
		Report: filepath.ToSlash(filepath.Join(".run", "evals", c.ID+"-report.json")),
	}
	start := time.Now()
	finish := func(v Verdict, reason string) CaseResult {
		res.Verdict = v
		res.DurationMS = time.Since(start).Milliseconds()
		res.Reason = reason
		switch v {
		case VerdictPass:
			fmt.Printf("  => PASS (%dms) status=%s root_cause=%s source=%s\n",
				res.DurationMS, res.Actual.Status, res.Actual.RootCause, res.Actual.DiagnosisSource)
		case VerdictSkip:
			fmt.Printf("  => SKIP (%s)\n", reason)
		case VerdictFail:
			fmt.Printf("  => FAIL (%dms) %s\n", res.DurationMS, reason)
		case VerdictError:
			fmt.Printf("  => ERROR (%dms) %s\n", res.DurationMS, reason)
		}
		return res
	}

	// Reset per-case state; the fault target from a previous case must be gone.
	h.ClearFault()

	if !c.Supports(h.mode) {
		return finish(VerdictSkip, fmt.Sprintf("requires %v, current mode=%s", c.Modes, h.mode))
	}

	recoverAndReset := func() {
		if err := c.Recover(ctx, h); err != nil {
			logf("[warn] recovery hook failed for %s: %v", c.ID, err)
		}
		h.ClearFault()
		if err := h.waitForCode(ctx, h.BaseURL()+"/health", 200, 30*time.Second); err != nil {
			logf("[warn] stack /health not 200 after recovery of %s", c.ID)
		}
	}

	if err := c.Inject(ctx, h); err != nil {
		recoverAndReset()
		return finish(VerdictError, "fault injection failed: "+err.Error())
	}
	if err := c.WaitFault(ctx, h); err != nil {
		recoverAndReset()
		return finish(VerdictError, "fault did not become observable within the wait window: "+err.Error())
	}

	raw, stderr, rc := h.runAgent(ctx, c)

	// Recover before evaluating so an assertion failure cannot strand the
	// stack in the faulted state for the next case.
	recoverAndReset()

	reportPath := filepath.Join(h.runDir, c.ID+"-report.json")
	_ = os.WriteFile(reportPath, raw, 0o644)
	if len(stderr) > 0 {
		_ = os.WriteFile(filepath.Join(h.runDir, c.ID+"-agent.stderr"), stderr, 0o644)
	}
	if rc != 0 {
		return finish(VerdictError, fmt.Sprintf("agent diagnose exited %d: %s", rc, truncate(strings.TrimSpace(string(stderr)), 300)))
	}

	var report agentReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return finish(VerdictError, "agent output is not valid Report JSON: "+err.Error())
	}
	res.Actual = Actual{
		Status:          report.Diagnosis.Status,
		RootCause:       report.Diagnosis.RootCause,
		DiagnosisSource: report.DiagnosisSource,
	}

	var diffs []string
	if res.Actual.Status != c.ExpectedStatus {
		diffs = append(diffs, fmt.Sprintf("status want=%q got=%q", c.ExpectedStatus, res.Actual.Status))
	}
	if res.Actual.RootCause != c.ExpectedRootCause {
		diffs = append(diffs, fmt.Sprintf("root_cause want=%q got=%q", c.ExpectedRootCause, res.Actual.RootCause))
	}
	if res.Actual.DiagnosisSource != expectedSource {
		diffs = append(diffs, fmt.Sprintf("diagnosis_source want=%q got=%q", expectedSource, res.Actual.DiagnosisSource))
	}
	if len(diffs) > 0 {
		return finish(VerdictFail, strings.Join(diffs, "; "))
	}
	return finish(VerdictPass, "")
}

// strippedEnv returns the process environment with model/tracing variables
// removed, so a shell's API keys cannot perturb the rules-only baseline.
func strippedEnv() []string {
	drop := map[string]bool{
		"OPENAI_API_KEY": true, "OPENAI_BASE_URL": true, "MODEL_NAME": true,
		"COZELOOP_API_TOKEN": true, "COZELOOP_WORKSPACE_ID": true,
	}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if ok && drop[key] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// runAgent executes the real bin/agent diagnose CLI end to end and returns
// stdout, stderr and exit code.
func (h *Harness) runAgent(ctx context.Context, c Case) ([]byte, []byte, int) {
	args := []string{"diagnose", "--base-url", h.targetURL()}
	if c.TimeoutMS > 0 {
		args = append(args, "--timeout-ms", fmt.Sprintf("%d", c.TimeoutMS))
	}
	runCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, filepath.Join(h.root, "bin", "agent"), args...)
	cmd.Dir = h.root
	cmd.Env = strippedEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	rc := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			rc = exitErr.ExitCode()
		} else {
			rc = -1
			fmt.Fprintf(&stderr, "%v", err)
		}
	}
	return stdout.Bytes(), stderr.Bytes(), rc
}

func (h *Harness) writeReport(results []CaseResult) (*SuiteReport, string) {
	sort.Slice(results, func(i, j int) bool { return results[i].Index < results[j].Index })
	totals := Totals{}
	for _, r := range results {
		switch r.Verdict {
		case VerdictPass:
			totals.Pass++
		case VerdictFail:
			totals.Fail++
		case VerdictSkip:
			totals.Skip++
		case VerdictError:
			totals.Error++
		}
	}
	suite := &SuiteReport{
		GeneratedAt: time.Now().UTC().Truncate(time.Second),
		Mode:        string(h.mode),
		BaseURL:     h.baseURL,
		Diagnoser:   "rules (model/tracing env stripped for hermetic eval)",
		Totals:      totals,
		Cases:       results,
	}
	data, _ := json.MarshalIndent(suite, "", "  ")

	latest := filepath.Join(h.runDir, "results-latest.json")
	stamped := filepath.Join(h.runDir, "results-"+time.Now().UTC().Format("20060102T150405Z")+".json")
	_ = os.WriteFile(latest, data, 0o644)
	_ = os.WriteFile(stamped, data, 0o644)
	return suite, latest
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
