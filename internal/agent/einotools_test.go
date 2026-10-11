package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPCheckToolInvokableRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"summary":"redis unavailable"}`))
	}))
	defer srv.Close()

	tl, err := NewHTTPCheckTool()
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(httpToolInput{URL: srv.URL + "/ready", TimeoutMS: 1000})
	out, err := tl.InvokableRun(context.Background(), string(args))
	if err != nil {
		t.Fatalf("tool must envelope failures, got err: %v", err)
	}
	var env toolEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("invalid envelope: %v (%s)", err, out)
	}
	if !env.OK {
		t.Fatalf("5xx must be ok envelope with result, got %+v", env)
	}
	res, _ := json.Marshal(env.Result)
	if !json.Valid(res) || string(res) == "" {
		t.Fatalf("missing result: %s", out)
	}
}

func TestHTTPCheckToolBadArgs(t *testing.T) {
	tl, _ := NewHTTPCheckTool()
	// Missing url: underlying tool returns a classified error envelope.
	out, err := tl.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("expected envelope, not framework error: %v", err)
	}
	var env toolEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("invalid envelope: %v", err)
	}
	if env.OK || env.ErrorType != "invalid_input" {
		t.Fatalf("want invalid_input envelope, got %+v", env)
	}
}
