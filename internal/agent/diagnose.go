package agent

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

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
	case isHealthyRedisContradiction(ev):
		return &model.Diagnosis{
			Status:           model.StatusInsufficientEvidence,
			RootCause:        model.CauseUnknown,
			Confidence:       0,
			Evidence:         collectedEvidenceItems(ev),
			SuggestedActions: []string{"HTTP endpoints report healthy while the redis container is observed exited; re-collect evidence"},
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
	containerDown := ev.RedisStatus != nil && ev.RedisStatus.OK && !ev.RedisStatus.Running
	if containerDown {
		return true
	}
	httpSignals := ev.Ready != nil && ev.Ready.OK && ev.Ready.StatusCode == 503 &&
		ev.Data != nil && ev.Data.OK && ev.Data.StatusCode == 500
	if httpSignals {
		return true
	}
	// Historical connection_refused log lines alone do not prove a current
	// outage: they may linger in the log tail long after recovery. They
	// only corroborate a currently failing signal.
	if ev.AppLogs != nil && ev.AppLogs.OK && hasLogError(ev.AppLogs, "connection_refused") &&
		(readyIs503(ev) || dataIs500(ev)) {
		return true
	}
	return false
}

func readyIs503(ev *model.CollectedEvidence) bool {
	return ev.Ready != nil && ev.Ready.OK && ev.Ready.StatusCode == 503
}

func dataIs500(ev *model.CollectedEvidence) bool {
	return ev.Data != nil && ev.Data.OK && ev.Data.StatusCode == 500
}

// isHealthyRedisContradiction detects ready/data both 200 while the redis
// container is explicitly observed exited: the signals disagree, so neither
// healthy nor a fault is concluded.
func isHealthyRedisContradiction(ev *model.CollectedEvidence) bool {
	return isHealthy(ev) && ev.RedisStatus != nil && ev.RedisStatus.OK && !ev.RedisStatus.Running
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
	containerDown := ev.RedisStatus != nil && ev.RedisStatus.OK && !ev.RedisStatus.Running
	if containerDown {
		out = append(out, model.EvidenceItem{
			Source: ToolContainerGet,
			Fact:   "redis container status is " + containerFact(ev.RedisStatus),
		})
	}
	// Logs only corroborate a currently observed anomaly; stale
	// connection_refused lines from a recovered outage are not evidence.
	if ev.AppLogs != nil && ev.AppLogs.OK && (containerDown || readyIs503(ev) || dataIs500(ev)) {
		for _, e := range ev.AppLogs.Errors {
			if e.Count > 0 {
				out = append(out, model.EvidenceItem{
					Source: ToolServiceLogs,
					Fact:   fmt.Sprintf("demo-api reported %s %d time(s), e.g. %s", e.ErrorType, e.Count, e.Sample),
				})
			}
		}
	}
	if readyIs503(ev) {
		out = append(out, model.EvidenceItem{
			Source: ToolCheckHTTP,
			Fact:   fmt.Sprintf("/ready returned %d", ev.Ready.StatusCode),
		})
	}
	if dataIs500(ev) {
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
// A model diagnosis is accepted only when its confidence, status, root
// cause and every evidence item are consistent with the collected
// observations; otherwise the rule-based result is returned so unsupported
// or fabricated conclusions never reach the caller.
func Validate(d *model.Diagnosis, ev *model.CollectedEvidence) (*model.Diagnosis, bool) {
	if d == nil {
		return DiagnoseRules(ev), false
	}
	expectedStatus, supported := expectedModelStatus(d.RootCause, ev)
	if !supported {
		return DiagnoseRules(ev), false
	}
	if d.Confidence < 0 || d.Confidence > 1 {
		return DiagnoseRules(ev), false
	}
	if strings.TrimSpace(d.Status) == "" {
		d.Status = expectedStatus
	} else if d.Status != expectedStatus {
		return DiagnoseRules(ev), false
	}
	if len(d.Evidence) == 0 {
		return DiagnoseRules(ev), false
	}
	for _, item := range d.Evidence {
		if !validEvidenceSource(item.Source) || strings.TrimSpace(item.Fact) == "" {
			return DiagnoseRules(ev), false
		}
		if !verifyEvidenceItem(item, ev) {
			return DiagnoseRules(ev), false
		}
	}
	if d.SuggestedActions == nil {
		d.SuggestedActions = []string{}
	}
	return d, true
}

// expectedModelStatus reports the status a model diagnosis must carry for
// its root cause and whether that root cause is the unique conclusion the
// rule-based diagnoser reaches for the same evidence. Mutually exclusive
// model conclusions (e.g. no_incident vs redis_not_running for a
// contradictory evidence set) cannot all pass: the model root cause must
// equal the rule baseline. An empty model status is filled with the
// returned default by the caller; an explicitly wrong status is rejected.
func expectedModelStatus(rootCause string, ev *model.CollectedEvidence) (string, bool) {
	ruleD := DiagnoseRules(ev)
	if rootCause != ruleD.RootCause {
		return "", false
	}
	switch ruleD.RootCause {
	case model.CauseRedisNotRunning, model.CauseAppStopped, model.CauseAppTimeout:
		return model.StatusDiagnosed, true
	case model.CauseNone:
		return model.StatusHealthy, true
	case model.CauseUnknown:
		return model.StatusInsufficientEvidence, true
	default:
		return "", false
	}
}

func validEvidenceSource(source string) bool {
	switch source {
	case ToolCheckHTTP, ToolServiceLogs, ToolContainerGet:
		return true
	}
	return false
}

var httpStatusCodeRe = regexp.MustCompile(`\b[1-5][0-9]{2}\b`)

// verifyEvidenceItem reports whether one model-provided evidence fact is
// directionally consistent with the collected observations. It uses simple
// keyword matching rather than NLP: a non-empty descriptive fact without
// extractable signals is accepted conservatively, while concrete claims
// (status codes, timeouts, failures, container state) must match.
func verifyEvidenceItem(item model.EvidenceItem, ev *model.CollectedEvidence) bool {
	switch item.Source {
	case ToolCheckHTTP:
		return verifyHTTPEvidence(item.Fact, ev)
	case ToolServiceLogs:
		return verifyLogEvidence(item.Fact, ev)
	case ToolContainerGet:
		return verifyContainerEvidence(item.Fact, ev)
	}
	return false
}

func verifyHTTPEvidence(fact string, ev *model.CollectedEvidence) bool {
	obs := map[string]*model.HTTPCheckObservation{
		"/health": ev.Health, "/ready": ev.Ready, "/api/data": ev.Data,
	}
	// A code appearing right after a mentioned path binds to that path's
	// observation, so "/ready returned 200" cannot pass when ready is 503
	// even if 200 was seen on another endpoint.
	for _, path := range []string{"/api/data", "/ready", "/health"} {
		idx := strings.Index(fact, path)
		if idx < 0 {
			continue
		}
		tail := fact[idx+len(path):]
		if loc := httpStatusCodeRe.FindStringIndex(tail); loc != nil && loc[0] <= 32 {
			code, err := strconv.Atoi(tail[loc[0]:loc[1]])
			if err != nil {
				return false
			}
			o := obs[path]
			if o == nil || !o.OK || o.StatusCode != code {
				return false
			}
		}
	}
	if codeTexts := httpStatusCodeRe.FindAllString(fact, -1); len(codeTexts) > 0 {
		// No bound path or the code precedes the path name: the code must
		// have been observed on at least one HTTP endpoint.
		for _, codeText := range codeTexts {
			code, err := strconv.Atoi(codeText)
			if err != nil {
				return false
			}
			seen := false
			for _, o := range obs {
				if o != nil && o.OK && o.StatusCode == code {
					seen = true
				}
			}
			if !seen {
				return false
			}
		}
		return true
	}
	lower := strings.ToLower(fact)
	if strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout") {
		for _, o := range obs {
			if o != nil && !o.OK && o.ErrorType == model.ErrorTypeTimeout {
				return true
			}
		}
		return false
	}
	if strings.Contains(lower, "unreachable") || strings.Contains(lower, "connection") {
		for _, o := range obs {
			if o != nil && !o.OK {
				return true
			}
		}
		return false
	}
	// Non-empty descriptive fact without a concrete signal: accept.
	return true
}

func verifyLogEvidence(fact string, ev *model.CollectedEvidence) bool {
	if ev.AppLogs == nil || !ev.AppLogs.OK {
		return false
	}
	lower := strings.ToLower(fact)
	for _, errorType := range []string{"connection_refused", "timeout"} {
		if strings.Contains(lower, errorType) && !hasLogError(ev.AppLogs, errorType) {
			return false
		}
	}
	return true
}

var (
	// containerDownClaimRe matches explicit stopped-state phrases ("down"
	// with word boundaries, or a running=false style boolean claim).
	containerDownClaimRe = regexp.MustCompile(`(?:\bdown\b|running\s*[:=]\s*false)`)
	// containerUpClaimRe matches explicit running-state phrases. It is only
	// consulted after down-claim matching, so "not running" can never be
	// misread as a running claim.
	containerUpClaimRe = regexp.MustCompile(`(?:\b(?:running|up|healthy|alive)\b|running\s*[:=]\s*true)`)
)

func verifyContainerEvidence(fact string, ev *model.CollectedEvidence) bool {
	lower := strings.ToLower(fact)
	// Stopped-state phrases take priority: "not running" must classify as a
	// down claim rather than a running claim.
	claimedDown := strings.Contains(lower, "not running") ||
		strings.Contains(lower, "exited") ||
		strings.Contains(lower, "stopped") ||
		containerDownClaimRe.MatchString(lower)
	claimedUp := !claimedDown && containerUpClaimRe.MatchString(lower)
	// Facts without any recognizable explicit container state are rejected
	// instead of accepted on mere observation presence.
	if !claimedDown && !claimedUp {
		return false
	}
	mentionsRedis := strings.Contains(lower, "redis")
	mentionsApp := strings.Contains(lower, "demo-api") || strings.Contains(lower, "app")
	var obs *model.ContainerObservation
	switch {
	case mentionsRedis:
		obs = ev.RedisStatus
	case mentionsApp:
		obs = ev.AppStatus
	default:
		// No specific container (redis/app) identified: reject.
		return false
	}
	// A missing observation or a failed tool call supports no claim.
	if obs == nil || !obs.OK {
		return false
	}
	if claimedDown {
		return !obs.Running
	}
	return obs.Running
}
