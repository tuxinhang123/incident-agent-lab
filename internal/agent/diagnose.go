package agent

import (
	"fmt"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

// DiagnoseRules is the deterministic, model-free diagnoser. It is both the
// baseline for evals and the fallback used when no model is configured or the
// model output fails rule validation.
func DiagnoseRules(ev *model.CollectedEvidence) *model.Diagnosis {
	switch {
	case isAppStopped(ev):
		return &model.Diagnosis{
			Status:     model.StatusDiagnosed,
			RootCause:  model.CauseAppStopped,
			Confidence: 0.95,
			Evidence: []model.EvidenceItem{
				{Source: ToolCheckHTTP, Fact: "/health is unreachable: " + failFact(ev.Health)},
				{Source: ToolContainerGet, Fact: "demo-api container status is " + containerFact(ev.AppStatus)},
			},
			SuggestedActions: []string{
				"Start the demo-api container",
				"Inspect demo-api logs for startup errors",
			},
		}
	case isEndpointTimeout(ev):
		return &model.Diagnosis{
			Status:     model.StatusDiagnosed,
			RootCause:  model.CauseAppTimeout,
			Confidence: 0.8,
			Evidence:   timeoutEvidence(ev),
			SuggestedActions: []string{
				"Check demo-api resource usage and downstream latency",
				"Retry the endpoint after the dependency recovers",
			},
		}
	case isRedisDown(ev):
		return &model.Diagnosis{
			Status:     model.StatusDiagnosed,
			RootCause:  model.CauseRedisNotRunning,
			Confidence: 0.96,
			Evidence:   redisDownEvidence(ev),
			SuggestedActions: []string{
				"Start the Redis container",
				"Verify the readiness endpoint after recovery",
			},
		}
	case isHealthy(ev):
		return &model.Diagnosis{
			Status:     model.StatusHealthy,
			RootCause:  model.CauseNone,
			Confidence: 0.99,
			Evidence: []model.EvidenceItem{
				{Source: ToolCheckHTTP, Fact: fmt.Sprintf("/ready returned %d", codeOf(ev.Ready))},
				{Source: ToolCheckHTTP, Fact: fmt.Sprintf("/api/data returned %d", codeOf(ev.Data))},
			},
			SuggestedActions: []string{},
		}
	default:
		return &model.Diagnosis{
			Status:           model.StatusInsufficientEvidence,
			RootCause:        model.CauseUnknown,
			Confidence:       0,
			Evidence:         collectedEvidenceItems(ev),
			SuggestedActions: []string{"Collect container status and service logs before concluding"},
		}
	}
}

func isAppStopped(ev *model.CollectedEvidence) bool {
	healthDown := ev.Health != nil && !ev.Health.OK
	appDown := ev.AppStatus != nil && ev.AppStatus.OK && !ev.AppStatus.Running
	return healthDown && appDown
}

func isEndpointTimeout(ev *model.CollectedEvidence) bool {
	for _, o := range []*model.HTTPCheckObservation{ev.Ready, ev.Data, ev.Health} {
		if o != nil && !o.OK && o.ErrorType == model.ErrorTypeTimeout {
			return true
		}
	}
	return false
}

func isRedisDown(ev *model.CollectedEvidence) bool {
	if ev.RedisStatus != nil && ev.RedisStatus.OK && !ev.RedisStatus.Running {
		return true
	}
	if ev.AppLogs != nil && ev.AppLogs.OK && hasLogError(ev.AppLogs, "connection_refused") {
		return true
	}
	if ev.Ready != nil && ev.Ready.OK && ev.Ready.StatusCode == 503 &&
		ev.Data != nil && ev.Data.OK && ev.Data.StatusCode == 500 {
		return true
	}
	return false
}

func isHealthy(ev *model.CollectedEvidence) bool {
	ready200 := ev.Ready != nil && ev.Ready.OK && ev.Ready.StatusCode == 200
	data200 := ev.Data != nil && ev.Data.OK && ev.Data.StatusCode == 200
	return ready200 && data200
}

func hasLogError(o *model.LogObservation, errorType string) bool {
	for _, e := range o.Errors {
		if e.ErrorType == errorType && e.Count > 0 {
			return true
		}
	}
	return false
}

func redisDownEvidence(ev *model.CollectedEvidence) []model.EvidenceItem {
	var out []model.EvidenceItem
	if ev.RedisStatus != nil && ev.RedisStatus.OK && !ev.RedisStatus.Running {
		out = append(out, model.EvidenceItem{
			Source: ToolContainerGet,
			Fact:   "redis container status is " + containerFact(ev.RedisStatus),
		})
	}
	if ev.AppLogs != nil && ev.AppLogs.OK {
		for _, e := range ev.AppLogs.Errors {
			if e.Count > 0 {
				out = append(out, model.EvidenceItem{
					Source: ToolServiceLogs,
					Fact:   fmt.Sprintf("demo-api reported %s %d time(s), e.g. %s", e.ErrorType, e.Count, e.Sample),
				})
			}
		}
	}
	if ev.Ready != nil && ev.Ready.OK {
		out = append(out, model.EvidenceItem{
			Source: ToolCheckHTTP,
			Fact:   fmt.Sprintf("/ready returned %d", ev.Ready.StatusCode),
		})
	}
	if ev.Data != nil && ev.Data.OK {
		out = append(out, model.EvidenceItem{
			Source: ToolCheckHTTP,
			Fact:   fmt.Sprintf("/api/data returned %d", ev.Data.StatusCode),
		})
	}
	return out
}

func timeoutEvidence(ev *model.CollectedEvidence) []model.EvidenceItem {
	var out []model.EvidenceItem
	for name, o := range map[string]*model.HTTPCheckObservation{
		"/health": ev.Health, "/ready": ev.Ready, "/api/data": ev.Data,
	} {
		if o != nil && !o.OK && o.ErrorType == model.ErrorTypeTimeout {
			out = append(out, model.EvidenceItem{Source: ToolCheckHTTP, Fact: name + " timed out"})
		}
	}
	return out
}

func collectedEvidenceItems(ev *model.CollectedEvidence) []model.EvidenceItem {
	var out []model.EvidenceItem
	add := func(source, fact string) {
		if fact != "" {
			out = append(out, model.EvidenceItem{Source: source, Fact: fact})
		}
	}
	for name, o := range map[string]*model.HTTPCheckObservation{
		"/health": ev.Health, "/ready": ev.Ready, "/api/data": ev.Data,
	} {
		if o == nil {
			continue
		}
		if o.OK {
			add(ToolCheckHTTP, fmt.Sprintf("%s returned %d", name, o.StatusCode))
		} else {
			add(ToolCheckHTTP, fmt.Sprintf("%s failed: %s", name, o.ErrorType))
		}
	}
	if ev.AppLogs != nil && ev.AppLogs.OK {
		add(ToolServiceLogs, fmt.Sprintf("scanned %d log line(s)", ev.AppLogs.LinesScanned))
	}
	for name, o := range map[string]*model.ContainerObservation{
		"redis": ev.RedisStatus, "demo-api": ev.AppStatus,
	} {
		if o != nil && o.OK {
			add(ToolContainerGet, fmt.Sprintf("%s container running=%v", name, o.Running))
		}
	}
	return out
}

func failFact(o *model.HTTPCheckObservation) string {
	if o == nil {
		return "no observation"
	}
	return o.ErrorType
}

func containerFact(o *model.ContainerObservation) string {
	if o == nil {
		return "unknown"
	}
	if o.Status != "" {
		return o.Status
	}
	return "unknown"
}

func codeOf(o *model.HTTPCheckObservation) int {
	if o == nil {
		return 0
	}
	return o.StatusCode
}

// Validate checks a model-produced diagnosis against the gathered evidence.
// A root cause without supporting evidence is rejected and replaced by the
// rule-based result so unsupported conclusions never reach the caller.
func Validate(d *model.Diagnosis, ev *model.CollectedEvidence) (*model.Diagnosis, bool) {
	if d == nil {
		return DiagnoseRules(ev), false
	}
	switch d.RootCause {
	case model.CauseRedisNotRunning:
		if !isRedisDown(ev) {
			return DiagnoseRules(ev), false
		}
		if len(d.Evidence) == 0 {
			d.Evidence = redisDownEvidence(ev)
		}
	case model.CauseAppStopped:
		if !isAppStopped(ev) {
			return DiagnoseRules(ev), false
		}
	case model.CauseAppTimeout:
		if !isEndpointTimeout(ev) {
			return DiagnoseRules(ev), false
		}
	case model.CauseNone:
		if !isHealthy(ev) {
			return DiagnoseRules(ev), false
		}
		d.Status = model.StatusHealthy
	default:
		return DiagnoseRules(ev), false
	}
	if d.Status == "" {
		d.Status = model.StatusDiagnosed
	}
	if d.Evidence == nil {
		d.Evidence = []model.EvidenceItem{}
	}
	if d.SuggestedActions == nil {
		d.SuggestedActions = []string{}
	}
	return d, true
}
