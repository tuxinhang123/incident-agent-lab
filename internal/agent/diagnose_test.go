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

func redisDownEvidenceFixture() *model.CollectedEvidence {
	return &model.CollectedEvidence{
		Health: &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Ready:  &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 503},
		Data:   &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 500},
		AppLogs: &model.LogObservation{Source: ToolServiceLogs, OK: true, Errors: []model.ErrorAggregate{
			{ErrorType: "connection_refused", Count: 12, Sample: "failed to connect to redis"},
		}},
		RedisStatus: &model.ContainerObservation{Source: ToolContainerGet, OK: true, Status: "exited", Running: false, ExitCode: intPtr(0)},
	}
}

func TestValidateRejectsEmptyEvidence(t *testing.T) {
	ev := redisDownEvidenceFixture()
	modelSays := &model.Diagnosis{
		Status: model.StatusDiagnosed, RootCause: model.CauseRedisNotRunning, Confidence: 0.9,
	}
	if _, ok := Validate(modelSays, ev); ok {
		t.Fatal("model diagnosis without evidence must be rejected")
	}
}

func TestValidateRejectsFabricatedFact(t *testing.T) {
	ev := redisDownEvidenceFixture()
	modelSays := &model.Diagnosis{
		Status: model.StatusDiagnosed, RootCause: model.CauseRedisNotRunning, Confidence: 0.9,
		Evidence: []model.EvidenceItem{{Source: ToolCheckHTTP, Fact: "/ready returned 200"}},
	}
	if _, ok := Validate(modelSays, ev); ok {
		t.Fatal("fabricated status code fact must be rejected")
	}
}

func TestValidateRejectsStatusMismatch(t *testing.T) {
	ev := redisDownEvidenceFixture()
	modelSays := &model.Diagnosis{
		Status: model.StatusHealthy, RootCause: model.CauseRedisNotRunning, Confidence: 0.9,
		Evidence: []model.EvidenceItem{{Source: ToolCheckHTTP, Fact: "/ready returned 503"}},
	}
	if _, ok := Validate(modelSays, ev); ok {
		t.Fatal("status contradicting root cause must be rejected")
	}
}

func TestValidateRejectsConfidenceOutOfRange(t *testing.T) {
	ev := redisDownEvidenceFixture()
	for _, conf := range []float64{1.5, -0.2} {
		modelSays := &model.Diagnosis{
			Status: model.StatusDiagnosed, RootCause: model.CauseRedisNotRunning, Confidence: conf,
			Evidence: []model.EvidenceItem{{Source: ToolCheckHTTP, Fact: "/ready returned 503"}},
		}
		if _, ok := Validate(modelSays, ev); ok {
			t.Fatalf("confidence %f out of [0,1] must be rejected", conf)
		}
	}
}

func TestValidateAcceptsValidModelConclusion(t *testing.T) {
	ev := redisDownEvidenceFixture()
	modelSays := &model.Diagnosis{
		Status: model.StatusDiagnosed, RootCause: model.CauseRedisNotRunning, Confidence: 0.87,
		Evidence: []model.EvidenceItem{
			{Source: ToolCheckHTTP, Fact: "/ready returned 503 and /api/data returned 500"},
			{Source: ToolServiceLogs, Fact: "logs show connection_refused errors"},
			{Source: ToolContainerGet, Fact: "redis container is exited"},
		},
	}
	got, ok := Validate(modelSays, ev)
	if !ok || got.RootCause != model.CauseRedisNotRunning {
		t.Fatalf("valid model conclusion should be accepted, got ok=%v %+v", ok, got)
	}
}

func TestValidateAcceptsDescriptiveFact(t *testing.T) {
	ev := redisDownEvidenceFixture()
	modelSays := &model.Diagnosis{
		Status: model.StatusDiagnosed, RootCause: model.CauseRedisNotRunning, Confidence: 0.5,
		Evidence: []model.EvidenceItem{{Source: ToolCheckHTTP, Fact: "the readiness probe was failing"}},
	}
	if _, ok := Validate(modelSays, ev); !ok {
		t.Fatal("non-empty descriptive fact without concrete signals should be accepted")
	}
}

func TestValidateEmptyStatusFilledFromRootCause(t *testing.T) {
	ev := redisDownEvidenceFixture()
	modelSays := &model.Diagnosis{
		RootCause: model.CauseRedisNotRunning, Confidence: 0.8,
		Evidence: []model.EvidenceItem{{Source: ToolCheckHTTP, Fact: "/ready returned 503"}},
	}
	got, ok := Validate(modelSays, ev)
	if !ok || got.Status != model.StatusDiagnosed {
		t.Fatalf("empty status should be defaulted to diagnosed, got ok=%v %+v", ok, got)
	}
}

func TestDiagnoseRulesHealthyIgnoresStaleLogs(t *testing.T) {
	ev := &model.CollectedEvidence{
		Health: &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Ready:  &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Data:   &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		AppLogs: &model.LogObservation{Source: ToolServiceLogs, OK: true, Errors: []model.ErrorAggregate{
			{ErrorType: "connection_refused", Count: 3, Sample: "failed to connect to redis"},
		}},
		RedisStatus: &model.ContainerObservation{Source: ToolContainerGet, OK: true, Status: "running", Running: true},
	}
	d := DiagnoseRules(ev)
	if d.Status != model.StatusHealthy || d.RootCause != model.CauseNone {
		t.Fatalf("want no_incident despite stale logs, got %s/%s", d.Status, d.RootCause)
	}
	for _, e := range d.Evidence {
		if e.Source == ToolServiceLogs {
			t.Fatalf("healthy conclusion must not cite stale log errors: %+v", d.Evidence)
		}
	}
}

func TestDiagnoseRulesRedisDownIncludesLogEvidenceWithCurrentSignals(t *testing.T) {
	ev := &model.CollectedEvidence{
		Health: &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Ready:  &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 503},
		Data:   &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 500},
		AppLogs: &model.LogObservation{Source: ToolServiceLogs, OK: true, Errors: []model.ErrorAggregate{
			{ErrorType: "connection_refused", Count: 9, Sample: "failed to connect to redis"},
		}},
		RedisStatus: &model.ContainerObservation{Source: ToolContainerGet, OK: false, ErrorType: model.ErrorTypeDockerUnavailable},
	}
	d := DiagnoseRules(ev)
	if d.RootCause != model.CauseRedisNotRunning {
		t.Fatalf("want redis_not_running, got %s", d.RootCause)
	}
	foundLog := false
	for _, e := range d.Evidence {
		if e.Source == ToolServiceLogs {
			foundLog = true
		}
	}
	if !foundLog {
		t.Fatalf("log evidence must accompany current 503/500 signals: %+v", d.Evidence)
	}
}

func TestDiagnoseRulesStaleLogsAloneDoNotFlagRedisDown(t *testing.T) {
	ev := &model.CollectedEvidence{
		Health: &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Ready:  &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: false, ErrorType: model.ErrorTypeConnectionFailed},
		Data:   &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: false, ErrorType: model.ErrorTypeConnectionFailed},
		AppLogs: &model.LogObservation{Source: ToolServiceLogs, OK: true, Errors: []model.ErrorAggregate{
			{ErrorType: "connection_refused", Count: 3, Sample: "failed to connect to redis"},
		}},
		RedisStatus: &model.ContainerObservation{Source: ToolContainerGet, OK: true, Status: "running", Running: true},
	}
	d := DiagnoseRules(ev)
	if d.RootCause == model.CauseRedisNotRunning {
		t.Fatalf("stale connection_refused logs without a current signal must not flag redis down, got %+v", d)
	}
}

func TestDiagnoseRulesHealthyButRedisExitedIsContradiction(t *testing.T) {
	ev := &model.CollectedEvidence{
		Health:      &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Ready:       &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Data:        &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		RedisStatus: &model.ContainerObservation{Source: ToolContainerGet, OK: true, Status: "exited", Running: false, ExitCode: intPtr(0)},
	}
	d := DiagnoseRules(ev)
	if d.Status != model.StatusInsufficientEvidence || d.RootCause != model.CauseUnknown || d.Confidence != 0 {
		t.Fatalf("want insufficient_evidence/unknown/0 for contradictory signals, got %s/%s/%f", d.Status, d.RootCause, d.Confidence)
	}
}

func TestVerifyContainerEvidenceRunningClaims(t *testing.T) {
	cases := []struct {
		name    string
		fact    string
		running bool
		ok      bool
		want    bool
	}{
		{"redis running claim but stopped", "redis container is running", false, true, false},
		{"redis running claim and running", "redis container is running", true, true, true},
		{"app up claim but stopped", "demo-api container is up", false, true, false},
		{"app up claim and running", "demo-api container is up", true, true, true},
		{"redis down claim but running", "redis container not running", true, true, false},
		{"redis down claim and stopped", "redis container not running", false, true, true},
		{"no state word rejected when running", "container inspected", true, true, false},
		{"no state word rejected when stopped", "container inspected", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := &model.CollectedEvidence{
				RedisStatus: &model.ContainerObservation{Source: ToolContainerGet, OK: tc.ok, Running: tc.running},
				AppStatus:   &model.ContainerObservation{Source: ToolContainerGet, OK: tc.ok, Running: tc.running},
			}
			if got := verifyContainerEvidence(tc.fact, ev); got != tc.want {
				t.Fatalf("verifyContainerEvidence(%q) = %v, want %v", tc.fact, got, tc.want)
			}
		})
	}
}

func TestVerifyContainerEvidenceToolFailureRejectsAllClaims(t *testing.T) {
	ev := &model.CollectedEvidence{
		RedisStatus: &model.ContainerObservation{Source: ToolContainerGet, OK: false, ErrorType: model.ErrorTypeDockerUnavailable},
		AppStatus:   &model.ContainerObservation{Source: ToolContainerGet, OK: false, ErrorType: model.ErrorTypeDockerUnavailable},
	}
	for _, fact := range []string{
		"redis container is running",
		"redis container not running",
		"demo-api container is up",
		"demo-api container exited",
	} {
		if verifyContainerEvidence(fact, ev) {
			t.Fatalf("claim %q must be rejected when the container tool failed", fact)
		}
	}
}

func TestValidateRejectsUnknownWhenRulesSayHealthy(t *testing.T) {
	ev := &model.CollectedEvidence{
		Ready: &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Data:  &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
	}
	modelSays := &model.Diagnosis{
		Status: model.StatusInsufficientEvidence, RootCause: model.CauseUnknown, Confidence: 0.3,
		Evidence: []model.EvidenceItem{{Source: ToolCheckHTTP, Fact: "/ready returned 200"}},
	}
	got, ok := Validate(modelSays, ev)
	if ok {
		t.Fatal("unknown must be rejected on healthy evidence even with verifiable facts")
	}
	if got.RootCause != model.CauseNone {
		t.Fatalf("rejected diagnosis must fall back to no_incident, got %s", got.RootCause)
	}
}

func contradictoryEvidenceFixture() *model.CollectedEvidence {
	return &model.CollectedEvidence{
		Health:      &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Ready:       &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		Data:        &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 200},
		RedisStatus: &model.ContainerObservation{Source: ToolContainerGet, OK: true, Status: "exited", Running: false, ExitCode: intPtr(0)},
	}
}

func TestValidateContradictionAcceptsOnlyUnknown(t *testing.T) {
	verifiableUnknownEvidence := []model.EvidenceItem{
		{Source: ToolCheckHTTP, Fact: "/ready returned 200 and /api/data returned 200"},
		{Source: ToolContainerGet, Fact: "redis container running=false"},
	}

	noIncident := &model.Diagnosis{
		Status: model.StatusHealthy, RootCause: model.CauseNone, Confidence: 0.8,
		Evidence: verifiableUnknownEvidence,
	}
	if got, ok := Validate(noIncident, contradictoryEvidenceFixture()); ok {
		t.Fatalf("no_incident must be rejected on contradictory evidence, got %+v", got)
	}

	redisDown := &model.Diagnosis{
		Status: model.StatusDiagnosed, RootCause: model.CauseRedisNotRunning, Confidence: 0.8,
		Evidence: verifiableUnknownEvidence,
	}
	if got, ok := Validate(redisDown, contradictoryEvidenceFixture()); ok {
		t.Fatalf("redis_not_running must be rejected on contradictory evidence, got %+v", got)
	}

	unknown := &model.Diagnosis{
		Status: model.StatusInsufficientEvidence, RootCause: model.CauseUnknown, Confidence: 0,
		Evidence: verifiableUnknownEvidence,
	}
	got, ok := Validate(unknown, contradictoryEvidenceFixture())
	if !ok || got.RootCause != model.CauseUnknown || got.Status != model.StatusInsufficientEvidence {
		t.Fatalf("unknown/insufficient_evidence with verifiable facts must be accepted, got ok=%v %+v", ok, got)
	}
}

func TestValidateFaultEvidenceRejectsUnknownAcceptsRedisDown(t *testing.T) {
	ev := &model.CollectedEvidence{
		Ready: &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 503},
		Data:  &model.HTTPCheckObservation{Source: ToolCheckHTTP, OK: true, StatusCode: 500},
	}
	unknown := &model.Diagnosis{
		Status: model.StatusInsufficientEvidence, RootCause: model.CauseUnknown, Confidence: 0.2,
		Evidence: []model.EvidenceItem{{Source: ToolCheckHTTP, Fact: "/ready returned 503"}},
	}
	if _, ok := Validate(unknown, ev); ok {
		t.Fatal("unknown must be rejected when rules diagnose a concrete fault")
	}
	redisDown := &model.Diagnosis{
		Status: model.StatusDiagnosed, RootCause: model.CauseRedisNotRunning, Confidence: 0.9,
		Evidence: []model.EvidenceItem{{Source: ToolCheckHTTP, Fact: "/ready returned 503 and /api/data returned 500"}},
	}
	if got, ok := Validate(redisDown, ev); !ok || got.RootCause != model.CauseRedisNotRunning {
		t.Fatalf("redis_not_running must stay accepted on fault evidence, got ok=%v %+v", ok, got)
	}
}
