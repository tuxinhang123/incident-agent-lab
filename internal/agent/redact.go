package agent

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// redactedValue replaces sensitive field values in audited records.
const redactedValue = "[REDACTED]"

// sensitiveQueryKeys lists query-parameter names (normalized: lowercase with
// '_' and '-' stripped) whose values must never reach the audit trail.
var sensitiveQueryKeys = map[string]struct{}{
	"token":       {},
	"accesstoken": {},
	"key":         {},
	"apikey":      {},
	"secret":      {},
	"signature":   {},
	"sig":         {},
	"password":    {},
	"passwd":      {},
	"accesskey":   {},
}

// embeddedURLRe finds http(s) URLs embedded inside free-form strings such as
// response body summaries, log samples or error messages.
var embeddedURLRe = regexp.MustCompile(`https?://[^\s"'<>` + "`" + `)\]]+`)

// redactArgs returns a copy of args in which every string value is scrubbed of
// embedded credentials. Maps and slices are traversed recursively; values of
// other types are preserved as-is. The input map is never mutated.
func redactArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = redactValue(v)
	}
	return out
}

// redactResult deeply scrubs a tool result before it is serialized into the
// audit trail. The value is round-tripped through generic JSON containers so
// that strings nested in structs (body_summary, sample, message, ...) are
// covered without reflection; values that cannot be JSON-decoded are returned
// unchanged and later rejected by json.Marshal as before.
func redactResult(result any) any {
	if result == nil {
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return result
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return result
	}
	return redactValue(generic)
}

// redactValue returns a sanitized deep copy for map/string/slice values.
func redactValue(v any) any {
	switch t := v.(type) {
	case string:
		return sanitizeString(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, sub := range t {
			out[k] = redactValue(sub)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, sub := range t {
			out[i] = redactValue(sub)
		}
		return out
	default:
		return v
	}
}

// sanitizeString scrubs credentials from URLs that either make up the whole
// string or are embedded in surrounding text. Strings without credentials are
// returned byte-for-byte unchanged.
func sanitizeString(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}
	// Fast path: the whole string is a single URL.
	if u, err := url.Parse(s); err == nil && isHTTPURL(u) {
		if r, changed := redactParsedURL(u); changed {
			return r
		}
		return s
	}
	// Slow path: URLs embedded inside free-form text.
	return embeddedURLRe.ReplaceAllStringFunc(s, func(match string) string {
		u, err := url.Parse(match)
		if err != nil || !isHTTPURL(u) {
			return match
		}
		if r, changed := redactParsedURL(u); changed {
			return r
		}
		return match
	})
}

func isHTTPURL(u *url.URL) bool {
	return u != nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

// redactParsedURL removes userinfo and sensitive query-parameter values.
// The second return reports whether any redaction was performed, so callers
// can keep the original string untouched for clean URLs.
func redactParsedURL(u *url.URL) (string, bool) {
	changed := false
	if u.User != nil {
		u.User = url.User("redacted")
		changed = true
	}
	if u.RawQuery != "" {
		q := u.Query()
		for key, vals := range q {
			if !isSensitiveQueryKey(key) {
				continue
			}
			for i := range vals {
				vals[i] = redactedValue
			}
			changed = true
		}
		if changed {
			u.RawQuery = q.Encode()
		}
	}
	if !changed {
		return "", false
	}
	return u.String(), true
}

func isSensitiveQueryKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
	_, ok := sensitiveQueryKeys[normalized]
	return ok
}
