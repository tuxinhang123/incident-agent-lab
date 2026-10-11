package agent

import (
	"testing"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

func intPtr(n int) *int { return &n }

func TestDiagnoseRulesRedisDown(t *testing.T) {
	ev := &model.CollectedEvidence{
		Health: &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Ready:  &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 503},
		Data:   &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 500},
		AppLogs: &model.LogObservation{Source: ToolServiceLogs, OK: true, Errors: []model.ErrorAggregate{
			{ErrorType: "connection_refused", Count: 12, Sample: "failed to connect to redis"},
		}},
		RedisStatus: &model.ContainerObservation{Source: ToolContainerGet, OK: true, Status: "exited", Running: false, ExitCode: intPtr(0)},
	}
	d := DiagnoseRules(ev)
	if d.RootCause != model.CauseRedisNotRunning || d.Status != model.StatusDiagnosed {
		t.Fatalf("want redis_not_running/diagnosed, got %s/%s", d.RootCause, d.Status)
	}
	if len(d.Evidence) < 3 {
		t.Fatalf("want evidence from all 3 tools, got %+v", d.Evidence)
	}
	sources := map[string]bool{}
	for _, e := range d.Evidence {
		sources[e.Source] = true
	}
	for _, want := range []string{ToolCheckHTTP, ToolServiceLogs, ToolContainerGet} {
		if !sources[want] {
			t.Fatalf("missing evidence from %s: %+v", want, d.Evidence)
		}
	}
}

func TestDiagnoseRulesRedisDownBySignalsWithoutDocker(t *testing.T) {
	// Local (no docker) mode: container tools fail but 503+500+logs prove it.
	ev := &model.CollectedEvidence{
		Health:      &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Ready:       &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 503},
		Data:        &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 500},
		AppLogs:     &model.LogObservation{Source: ToolServiceLogs, OK: false, ErrorType: model.ErrorTypeDockerUnavailable},
		RedisStatus: &model.ContainerObservation{Source: ToolContainerGet, OK: false, ErrorType: model.ErrorTypeDockerUnavailable},
	}
	d := DiagnoseRules(ev)
	if d.RootCause != model.CauseRedisNotRunning {
		t.Fatalf("want redis_not_running from 503/500 signals, got %s", d.RootCause)
	}
}

func TestDiagnoseRulesHealthy(t *testing.T) {
	ev := &model.CollectedEvidence{
		Ready: &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Data:  &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
	}
	d := DiagnoseRules(ev)
	if d.Status != model.StatusHealthy || d.RootCause != model.CauseNone {
		t.Fatalf("want no_incident, got %s/%s", d.Status, d.RootCause)
	}
}

func TestDiagnoseRulesInsufficientEvidence(t *testing.T) {
	d := DiagnoseRules(&model.CollectedEvidence{})
	if d.Status != model.StatusInsufficientEvidence || d.RootCause != model.CauseUnknown {
		t.Fatalf("want insufficient_evidence/unknown, got %s/%s", d.Status, d.RootCause)
	}
	if d.Confidence != 0 {
		t.Fatalf("unknown cause must have zero confidence, got %f", d.Confidence)
	}
}

func TestDiagnoseRulesAppStopped(t *testing.T) {
	ev := &model.CollectedEvidence{
		Health:    &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: false, ErrorType: model.ErrorTypeConnectionFailed},
		AppStatus: &model.ContainerObservation{Source: ToolContainerGet, OK: true, Status: "exited", Running: false, ExitCode: intPtr(1)},
	}
	d := DiagnoseRules(ev)
	if d.RootCause != model.CauseAppStopped {
		t.Fatalf("want app_stopped, got %s", d.RootCause)
	}
}

func TestDiagnoseRulesTimeout(t *testing.T) {
	ev := &model.CollectedEvidence{
		Health: &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Ready:  &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: false, ErrorType: model.ErrorTypeTimeout},
	}
	d := DiagnoseRules(ev)
	if d.RootCause != model.CauseAppTimeout {
		t.Fatalf("want app_endpoint_timeout, got %s", d.RootCause)
	}
}

func TestValidateRejectsUnsupportedRootCause(t *testing.T) {
	ev := &model.CollectedEvidence{
		Ready: &model.HTTPCheckObservation{OK: true, StatusCode: 200},
		Data:  &model.HTTPCheckObservation{OK: true, StatusCode: 200},
	}
	// Model claims redis is down while evidence says healthy.
	modelSays := &model.Diagnosis{
		Status: model.StatusDiagnosed, RootCause: model.CauseRedisNotRunning, Confidence: 0.9,
	}
	got, ok := Validate(modelSays, ev)
	if ok {
		t.Fatal("unsupported model conclusion must be rejected")
	}
	if got.RootCause != model.CauseNone {
		t.Fatalf("rejected diagnosis must fall back to rules (no_incident), got %s", got.RootCause)
	}
}

func TestValidateAcceptsSupportedRootCause(t *testing.T) {
	ev := &model.CollectedEvidence{
		Ready: &model.HTTPCheckObservation{OK: true, StatusCode: 503},
		Data:  &model.HTTPCheckObservation{OK: true, StatusCode: 500},
	}
	modelSays := &model.Diagnosis{
		Status: model.StatusDiagnosed, RootCause: model.CauseRedisNotRunning, Confidence: 0.87,
		Evidence: []model.EvidenceItem{{Source: ToolCheckHTTP, Fact: "/ready returned 503"}},
	}
	got, ok := Validate(modelSays, ev)
	if !ok || got.RootCause != model.CauseRedisNotRunning {
		t.Fatalf("supported conclusion should pass validation, got ok=%v %+v", ok, got)
	}
}

func TestParseModelDiagnosis(t *testing.T) {
	content := "Here is my diagnosis:\n```json\n" + `{"status":"diagnosed","root_cause":"redis_not_running","confidence":0.9,"evidence":[],"suggested_actions":["x"]}` + "\n```"
	d := parseModelDiagnosis(content)
	if d == nil || d.RootCause != "redis_not_running" {
		t.Fatalf("failed to parse model content: %+v", d)
	}
	if parseModelDiagnosis("not json at all") != nil {
		t.Fatal("non-JSON content must return nil")
	}
}
