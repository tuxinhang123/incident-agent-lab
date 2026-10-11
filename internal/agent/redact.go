package agent

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// redactedValue replaces sensitive field values in audited records.
const redactedValue = "[REDACTED]"

// sensitiveKeys lists credential key names (normalized: lowercase with '_'
// and '-' stripped) whose values must never reach the audit trail. The same
// set backs URL query masking, fallback text masking and JSON field masking.
var sensitiveKeys = map[string]struct{}{
	"token":        {},
	"accesstoken":  {},
	"refreshtoken": {},
	"key":          {},
	"apikey":       {},
	"secret":       {},
	"clientsecret": {},
	"signature":    {},
	"sig":          {},
	"password":     {},
	"passwd":       {},
	"accesskey":    {},
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
	// Route the whole map through redactValue so top-level keys participate
	// in sensitive-key matching the same way as nested maps; otherwise a
	// credential passed as a top-level audit arg (e.g. {"token": "..."})
	// would keep its value.
	if out, ok := redactValue(args).(map[string]any); ok {
		return out
	}
	return make(map[string]any, len(args))
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
			if isSensitiveKey(k) {
				out[k] = redactSensitiveFieldValue(sub)
				continue
			}
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

// sanitizeString scrubs credentials from strings persisted in the audit
// trail. It covers whole and embedded http(s) URLs, structured JSON payloads
// and free-text credential assignments. Strings without credentials are
// returned unchanged.
func sanitizeString(s string) string {
	// Structured JSON payloads take precedence even when they contain "://"
	// (e.g. a credential URL held under "access_token"): redactValue masks
	// the whole value of sensitive keys, which URL-segment masking alone
	// cannot guarantee. JSON that fails to decode falls through to the
	// URL/text paths below.
	if r, ok := redactJSONString(s); ok {
		return r
	}
	if strings.Contains(s, "://") {
		// Fast path: the whole string is a single URL.
		if u, err := url.Parse(s); err == nil && isHTTPURL(u) {
			if r, changed := redactParsedURL(u); changed {
				return r
			}
			return s
		}
		// Slow path: URLs embedded inside free-form text.
		return redactEmbeddedURLs(s)
	}
	return redactNonURLString(s)
}

// redactJSONString decodes a JSON object/array payload, redacts it
// field-by-field and re-encodes it. The boolean is false when s is not a
// JSON object/array or cannot be decoded, leaving the caller's fallback free
// to handle it.
func redactJSONString(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	var generic any
	if err := json.Unmarshal([]byte(s), &generic); err != nil {
		return "", false
	}
	raw, err := json.Marshal(redactValue(generic))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// redactNonURLString scrubs strings without a URL scheme. Structured JSON
// payloads are decoded and redacted field-by-field so non-sensitive fields
// keep their values; other text gets conservative assignment masking that
// does not depend on URL parsing.
func redactNonURLString(s string) string {
	if r, ok := redactJSONString(s); ok {
		return r
	}
	return redactSensitiveText(s)
}

// redactEmbeddedURLs redacts URL spans inside free-form text: parsable URLs
// go through redactParsedURL while unparsable ones get conservative text
// masking; the same masking is applied to the surrounding non-URL fragments.
func redactEmbeddedURLs(s string) string {
	indexes := embeddedURLRe.FindAllStringIndex(s, -1)
	if len(indexes) == 0 {
		return redactSensitiveText(s)
	}
	var b strings.Builder
	prev := 0
	for _, loc := range indexes {
		b.WriteString(redactSensitiveText(s[prev:loc[0]]))
		match := s[loc[0]:loc[1]]
		if u, err := url.Parse(match); err == nil && isHTTPURL(u) {
			if r, changed := redactParsedURL(u); changed {
				b.WriteString(r)
			} else {
				b.WriteString(match)
			}
		} else {
			b.WriteString(redactSensitiveText(match))
		}
		prev = loc[1]
	}
	b.WriteString(redactSensitiveText(s[prev:]))
	return b.String()
}

// sensitiveAssignRe matches credential assignments in free text: token=value,
// "client_secret": "value" and key: value forms. Groups: 1 opening quote,
// 2 key, 3 closing quote and spacing, 4 separator, 5 spacing, 6 value.
// redactSensitiveText re-scans from the end of every non-sensitive key so a
// non-match (e.g. an "https://host" span) cannot swallow a later credential.
var sensitiveAssignRe = regexp.MustCompile(`(["']?)([A-Za-z0-9_-]+)(["']?\s*)([:=])(\s*)("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|[^\s&;,"'<>\]\)}]+)`)

// redactSensitiveText masks values assigned to sensitive keys without relying
// on URL parsing; it is the conservative fallback for malformed URLs and
// non-URL text.
func redactSensitiveText(s string) string {
	var b strings.Builder
	pos := 0
	lastWritten := 0
	for {
		loc := sensitiveAssignRe.FindStringSubmatchIndex(s[pos:])
		if loc == nil {
			break
		}
		keyEnd := pos + loc[5] // group 2 end
		key := s[pos+loc[4] : keyEnd]
		if !isSensitiveKey(key) {
			// Resume right after the key so assignments nested later in the
			// same candidate span are still examined.
			pos = keyEnd
			continue
		}
		valStart, valEnd := pos+loc[12], pos+loc[13] // group 6: value
		b.WriteString(s[lastWritten:valStart])
		value := s[valStart:valEnd]
		if value[0] == '"' || value[0] == '\'' {
			b.WriteByte(value[0])
			b.WriteString(redactedValue)
			b.WriteByte(value[0])
		} else {
			b.WriteString(redactedValue)
		}
		pos = valEnd
		lastWritten = valEnd
	}
	if lastWritten == 0 {
		return s
	}
	b.WriteString(s[lastWritten:])
	return b.String()
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
		// ParseQuery (unlike URL.Query) surfaces malformed pairs instead of
		// silently dropping them: bad percent-escapes and, on Go 1.17+,
		// semicolon separators return an error together with the dropped
		// pairs. Fall back to raw-text masking on error so credentials in
		// malformed queries cannot pass through untouched.
		q, err := url.ParseQuery(u.RawQuery)
		queryChanged := false
		if err == nil {
			for key, vals := range q {
				if !isSensitiveKey(key) {
					continue
				}
				for i := range vals {
					vals[i] = redactedValue
				}
				queryChanged = true
			}
			if queryChanged {
				u.RawQuery = q.Encode()
			}
		} else if masked := redactSensitiveText(u.RawQuery); masked != u.RawQuery {
			u.RawQuery = masked
			queryChanged = true
		}
		changed = changed || queryChanged
	}
	if !changed {
		return "", false
	}
	return u.String(), true
}

// isSensitiveKey reports whether key names a credential after normalization
// (lowercase with '_' and '-' stripped). Query masking, fallback text masking
// and JSON field masking all share this single predicate.
func isSensitiveKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
	_, ok := sensitiveKeys[normalized]
	return ok
}

// redactSensitiveFieldValue masks a JSON value held under a sensitive key:
// scalar values are replaced wholesale while containers keep their structure
// but every scalar leaf inside is masked.
func redactSensitiveFieldValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, sub := range t {
			out[k] = redactSensitiveFieldValue(sub)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, sub := range t {
			out[i] = redactSensitiveFieldValue(sub)
		}
		return out
	default:
		return redactedValue
	}
}
