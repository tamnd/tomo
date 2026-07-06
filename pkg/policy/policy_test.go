package policy

import (
	"testing"

	"github.com/tamnd/tomo/pkg/tool"
)

func TestClassDefaults(t *testing.T) {
	e := New(Config{})
	cases := []struct {
		class tool.Class
		want  Decision
	}{
		{tool.ClassRead, Allow},
		{tool.ClassNet, Allow},
		{tool.ClassWrite, Ask},
		{tool.ClassExec, Ask},
	}
	for _, c := range cases {
		if got, _ := e.Decide("some_tool", c.class, false); got != c.want {
			t.Errorf("%s default = %s, want %s", c.class, got, c.want)
		}
	}
}

func TestTaintEscalatesWriteAndExec(t *testing.T) {
	// Allow writes and exec outright, then prove taint pulls them back to ask.
	e := New(Config{Write: "allow", Exec: "allow"})

	if got, _ := e.Decide("write_file", tool.ClassWrite, false); got != Allow {
		t.Errorf("clean write = %s, want allow", got)
	}
	if got, reason := e.Decide("write_file", tool.ClassWrite, true); got != Ask {
		t.Errorf("tainted write = %s (%s), want ask", got, reason)
	}
	if got, _ := e.Decide("shell", tool.ClassExec, true); got != Ask {
		t.Errorf("tainted exec = %s, want ask", got)
	}
	// Reads and net are unaffected by taint.
	if got, _ := e.Decide("read_file", tool.ClassRead, true); got != Allow {
		t.Errorf("tainted read = %s, want allow", got)
	}
}

func TestPerToolRuleWinsAndIsNotEscalated(t *testing.T) {
	e := New(Config{Exec: "allow", Rules: map[string]string{"shell": "deny", "trusted_writer": "allow"}})

	if got, _ := e.Decide("shell", tool.ClassExec, false); got != Deny {
		t.Errorf("rule deny not applied: %s", got)
	}
	// An explicit allow rule is the user's considered choice; taint does not
	// second-guess it.
	if got, _ := e.Decide("trusted_writer", tool.ClassWrite, true); got != Allow {
		t.Errorf("explicit rule should survive taint, got %s", got)
	}
}

func TestExternalDefaultsToAsk(t *testing.T) {
	e := New(Config{})
	e.MarkExternal("srv_echo", "reddit_user")

	// A read from an external tool would normally allow, but external pulls it
	// to ask.
	if got, reason := e.Decide("srv_echo", tool.ClassRead, false); got != Ask {
		t.Errorf("external read = %s (%s), want ask", got, reason)
	}
	if got, _ := e.Decide("reddit_user", tool.ClassNet, false); got != Ask {
		t.Errorf("external net = %s, want ask", got)
	}
	// A tomo tool of the same class is unaffected.
	if got, _ := e.Decide("read_file", tool.ClassRead, false); got != Allow {
		t.Errorf("builtin read = %s, want allow", got)
	}
}

func TestExternalRuleWins(t *testing.T) {
	// A user who trusts one external tool can allow it outright; the rule wins
	// over the external default.
	e := New(Config{Rules: map[string]string{"srv_echo": "allow"}})
	e.MarkExternal("srv_echo")
	if got, _ := e.Decide("srv_echo", tool.ClassRead, false); got != Allow {
		t.Errorf("explicit allow should beat external default, got %s", got)
	}
}

func TestUnknownClassFailsClosed(t *testing.T) {
	e := New(Config{})
	if got, _ := e.Decide("weird", tool.Class("mystery"), false); got != Ask {
		t.Errorf("unknown class = %s, want ask", got)
	}
}

func TestParseDecisionDefaultsToAsk(t *testing.T) {
	for _, s := range []string{"", "maybe", "ALLOWish"} {
		if got := ParseDecision(s); got != Ask {
			t.Errorf("ParseDecision(%q) = %s, want ask", s, got)
		}
	}
	if ParseDecision("ALLOW") != Allow || ParseDecision(" deny ") != Deny {
		t.Error("valid decisions should parse case- and space-insensitively")
	}
}
