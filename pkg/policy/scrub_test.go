package policy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScrubRedactsSecrets(t *testing.T) {
	in := json.RawMessage(`{
		"path": "/etc/hosts",
		"api_key": "sk-abcdef0123456789abcdef",
		"headers": {"Authorization": "Bearer secrettokenvalue12345"},
		"nested": [{"token": "xoxb-1111-2222-abcdefghij"}],
		"note": "this is a normal argument"
	}`)
	out := string(Scrub(in))

	for _, leaked := range []string{"sk-abcdef0123456789abcdef", "secrettokenvalue12345", "xoxb-1111-2222-abcdefghij"} {
		if strings.Contains(out, leaked) {
			t.Errorf("scrubbed output still contains secret %q: %s", leaked, out)
		}
	}
	if !strings.Contains(out, "/etc/hosts") || !strings.Contains(out, "normal argument") {
		t.Errorf("scrubber dropped a non-secret field: %s", out)
	}
	if strings.Count(out, redacted) < 3 {
		t.Errorf("expected at least 3 redactions, got: %s", out)
	}
}

func TestScrubValueShapeWithoutKey(t *testing.T) {
	// A bare JWT in an argument that is not named like a secret still goes.
	in := json.RawMessage(`{"arg": "eyJhbGci.eyJzdWIi.SflKxwRJ"}`)
	if strings.Contains(string(Scrub(in)), "eyJhbGci") {
		t.Error("JWT-shaped value was not redacted")
	}
}

func TestScrubPassesThroughNonJSON(t *testing.T) {
	in := json.RawMessage(`not json at all`)
	if got := string(Scrub(in)); got != "not json at all" {
		t.Errorf("non-JSON input changed: %q", got)
	}
	if got := Scrub(nil); got != nil {
		t.Errorf("nil input = %v, want nil", got)
	}
}
