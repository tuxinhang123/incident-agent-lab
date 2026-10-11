package tools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuxinhang123/incident-agent-lab/internal/model"
)

func TestCheckHTTPEndpointSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	res, err := CheckHTTPEndpoint(HTTPEndpointInput{URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("want 200, got %d", res.StatusCode)
	}
	if res.BodySummary != `{"status":"ok"}` {
		t.Fatalf("unexpected body: %q", res.BodySummary)
	}
}

func TestCheckHTTPEndpointNon2xxIsResultNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "redis unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	res, err := CheckHTTPEndpoint(HTTPEndpointInput{URL: srv.URL})
	if err != nil {
		t.Fatalf("5xx should be a result, not error: %v", err)
	}
	if res.StatusCode != 503 {
		t.Fatalf("want 503, got %d", res.StatusCode)
	}
	if !strings.Contains(res.BodySummary, "redis unavailable") {
		t.Fatalf("unexpected body summary: %q", res.BodySummary)
	}
}

func TestCheckHTTPEndpointInvalidInput(t *testing.T) {
	cases := []HTTPEndpointInput{
		{URL: ""},
		{URL: "http://x", TimeoutMS: httpMaxTimeoutMS + 1},
	}
	for i, in := range cases {
		_, err := CheckHTTPEndpoint(in)
		if err == nil || err.Type != model.ErrorTypeInvalidInput {
			t.Fatalf("case %d: want invalid_input, got %+v", i, err)
		}
	}
}

func TestCheckHTTPEndpointConnectionFailed(t *testing.T) {
	ln := mustListenClosed(t)
	_, err := CheckHTTPEndpoint(HTTPEndpointInput{URL: "http://" + ln, TimeoutMS: 1000})
	if err == nil || err.Type != model.ErrorTypeConnectionFailed {
		t.Fatalf("want connection_failed, got %+v", err)
	}
}

func TestSummarizeBodyTruncatesAndFlattens(t *testing.T) {
	long := strings.Repeat("a", bodySummaryMaxChars+50)
	got := summarizeBody("  line1\n\t line2  \n" + long)
	if len(got) > bodySummaryMaxChars+3+20 {
		t.Fatalf("summary too long: %d", len(got))
	}
	if !strings.HasPrefix(got, "line1 line2 ") {
		t.Fatalf("whitespace not flattened: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
}
