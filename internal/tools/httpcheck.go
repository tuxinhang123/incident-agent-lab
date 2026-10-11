package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

const (
	httpDefaultTimeoutMS = 3000
	httpMaxTimeoutMS     = 10000
	httpMaxBodyBytes     = 4096
	bodySummaryMaxChars  = 200
)

type HTTPEndpointInput struct {
	URL       string `json:"url"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

type HTTPEndpointResult struct {
	StatusCode  int    `json:"status_code"`
	LatencyMS   int64  `json:"latency_ms"`
	BodySummary string `json:"body_summary"`
}

// CheckHTTPEndpoint performs a GET and reports status code, latency and a
// truncated single-line body summary. Non-2xx status codes are returned as
// regular results, not errors; transport failures are classified.
func CheckHTTPEndpoint(in HTTPEndpointInput) (*HTTPEndpointResult, *model.ToolError) {
	if in.URL == "" {
		return nil, &model.ToolError{Type: model.ErrorTypeInvalidInput, Message: "url is required"}
	}
	timeoutMS := in.TimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = httpDefaultTimeoutMS
	}
	if timeoutMS > httpMaxTimeoutMS {
		return nil, &model.ToolError{
			Type:    model.ErrorTypeInvalidInput,
			Message: fmt.Sprintf("timeout_ms must be <= %d", httpMaxTimeoutMS),
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return nil, &model.ToolError{Type: model.ErrorTypeInvalidInput, Message: err.Error()}
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, &model.ToolError{Type: model.ErrorTypeTimeout, Message: "request timed out"}
		}
		return nil, &model.ToolError{Type: model.ErrorTypeConnectionFailed, Message: err.Error()}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, httpMaxBodyBytes))
	return &HTTPEndpointResult{
		StatusCode:  resp.StatusCode,
		LatencyMS:   latency,
		BodySummary: summarizeBody(string(body)),
	}, nil
}

func summarizeBody(body string) string {
	s := strings.TrimSpace(body)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > bodySummaryMaxChars {
		s = s[:bodySummaryMaxChars] + "..."
	}
	return s
}
