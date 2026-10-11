package agent

import (
	"strings"
	"testing"
)

func TestSanitizeString(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "userinfo removed",
			in:   "https://alice:sekret@example.com/path",
			want: "https://redacted@example.com/path",
		},
		{
			name: "token query redacted",
			in:   "https://example.com/path?token=abc123&q=1",
			want: "https://example.com/path?q=1&token=%5BREDACTED%5D",
		},
		{
			name: "access_key query redacted",
			in:   "https://example.com/?access_key=AKIASECRET&next=2",
			want: "https://example.com/?access_key=%5BREDACTED%5D&next=2",
		},
		{
			name: "userinfo and token together",
			in:   "https://alice:sekret@example.com/path?token=abc123&q=1",
			want: "https://redacted@example.com/path?q=1&token=%5BREDACTED%5D",
		},
		{
			name: "clean URL unchanged",
			in:   "https://example.com/path?q=1&page=2",
			want: "https://example.com/path?q=1&page=2",
		},
		{
			name: "non-URL string unchanged",
			in:   "redis unavailable",
			want: "redis unavailable",
		},
		{
			name: "embedded URL in body text",
			in:   `upstream returned "visit https://bob:hunter2@api.internal/x?sig=zzz&ok=true for details"`,
			want: `upstream returned "visit https://redacted@api.internal/x?ok=true&sig=%5BREDACTED%5D for details"`,
		},
		{
			name: "case-insensitive sensitive key",
			in:   "https://example.com/?Token=abc&Secret=def",
			want: "https://example.com/?Secret=%5BREDACTED%5D&Token=%5BREDACTED%5D",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeString(tc.in); got != tc.want {
				t.Errorf("sanitizeString(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactArgs(t *testing.T) {
	in := map[string]any{
		"url":    "https://alice:sekret@example.com/path?token=abc123&q=1",
		"plain":  "ok",
		"number": 42,
		"nested": map[string]any{
			"callback": "https://x.test/?access_token=tkn",
			"items":    []any{"https://u:p@h.test/a", "clean"},
		},
	}
	out := redactArgs(in)

	gotURL, _ := out["url"].(string)
	if strings.Contains(gotURL, "alice") || strings.Contains(gotURL, "sekret") || strings.Contains(gotURL, "abc123") {
		t.Errorf("credentials leaked in redacted url: %q", gotURL)
	}
	if !strings.Contains(gotURL, "q=1") {
		t.Errorf("benign query parameter lost: %q", gotURL)
	}
	if out["plain"] != "ok" || out["number"] != 42 {
		t.Errorf("non-sensitive values altered: %#v", out)
	}
	nested, _ := out["nested"].(map[string]any)
	if nested == nil {
		t.Fatalf("nested map missing: %#v", out["nested"])
	}
	if got, _ := nested["callback"].(string); strings.Contains(got, "tkn") {
		t.Errorf("token leaked in nested value: %q", got)
	}
	items, _ := nested["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("nested slice altered: %#v", nested["items"])
	}
	if got, _ := items[0].(string); strings.Contains(got, "u:p") {
		t.Errorf("credentials leaked in nested slice: %q", got)
	}
	if items[1] != "clean" {
		t.Errorf("clean slice element altered: %#v", items[1])
	}
	// Original input must not be mutated.
	if in["url"] != "https://alice:sekret@example.com/path?token=abc123&q=1" {
		t.Errorf("input args were mutated: %q", in["url"])
	}
}

func TestRedactResult(t *testing.T) {
	type httpResult struct {
		StatusCode  int    `json:"status_code"`
		BodySummary string `json:"body_summary"`
	}
	in := httpResult{
		StatusCode:  200,
		BodySummary: `{"status":"ok","url":"https://svc:pw@host.internal/h?token=abc123&q=1"}`,
	}
	out := redactResult(in)
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("redactResult did not return generic map: %#v", out)
	}
	body, _ := m["body_summary"].(string)
	if strings.Contains(body, "svc") || strings.Contains(body, "pw") || strings.Contains(body, "abc123") {
		t.Errorf("credentials leaked in redacted body_summary: %q", body)
	}
	if !strings.Contains(body, "q=1") {
		t.Errorf("benign query parameter lost in body_summary: %q", body)
	}
	if m["status_code"] != float64(200) {
		t.Errorf("non-string field altered: %#v", m["status_code"])
	}
}

func TestRedactResultNil(t *testing.T) {
	if redactResult(nil) != nil {
		t.Errorf("redactResult(nil) != nil")
	}
	if redactArgs(nil) != nil {
		t.Errorf("redactArgs(nil) != nil")
	}
}
