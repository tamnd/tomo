package policy

import (
	"encoding/json"
	"regexp"
	"strings"
)

// redacted replaces any value the scrubber decides is a secret. It is a fixed
// marker so a reader of the log can tell a credential was present and withheld,
// rather than silently dropped.
const redacted = "[redacted]"

// secretKey matches an object key whose value is a credential by name. Tool
// inputs carry these as fields (an MCP server's api_key, a fetch tool's
// Authorization header), and their values must never reach the audit log.
var secretKey = regexp.MustCompile(`(?i)(authorization|api[_-]?key|secret|password|passwd|token|credential|private[_-]?key|bearer)`)

// secretValue matches a credential by shape, for the cases a key name does not
// give it away: a Bearer header, an sk-/ghp_-style token, a JWT. It is kept
// deliberately narrow so ordinary arguments (paths, commands, prose) are not
// mangled; the key-name rule is the primary defense.
var secretValue = regexp.MustCompile(`^(Bearer\s+\S+|(sk|ghp|gho|github_pat|xox[baprs])[-_][A-Za-z0-9._-]{16,}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)$`)

// Scrub returns a copy of a tool input with credential-shaped values replaced
// by a marker, so the audit log can record what ran without ever storing a
// secret it carried. A visibility feature that leaks keys is worse than none;
// this is the boundary that keeps `tomo watch` and audit.log safe to read.
//
// Input that is not valid JSON is returned unchanged: the auditor writes what
// the gate saw, and we do not have a structure to reason over. The common case,
// a tool's JSON arguments, is walked field by field.
func Scrub(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(scrubValue("", v))
	if err != nil {
		return raw
	}
	return out
}

// scrubValue walks a decoded JSON value. key is the object key this value sits
// under, empty at the top level, so a value is redacted either because its key
// names a secret or because the value itself is shaped like one.
func scrubValue(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = scrubValue(k, val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = scrubValue(key, val)
		}
		return t
	case string:
		if isSecret(key, t) {
			return redacted
		}
		return t
	default:
		return v
	}
}

func isSecret(key, val string) bool {
	if key != "" && secretKey.MatchString(key) {
		return true
	}
	return secretValue.MatchString(strings.TrimSpace(val))
}
