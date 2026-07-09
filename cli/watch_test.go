package cli

import (
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/tool"
)

func TestRenderEntry(t *testing.T) {
	allowed := true
	cases := []struct {
		name  string
		entry policy.Entry
		want  []string // substrings that must appear
	}{
		{
			name: "allowed read",
			entry: policy.Entry{
				Time: "2026-07-09T15:04:05Z", Tool: "read_file",
				Class: tool.ClassRead, Decision: policy.Allow, Allowed: true,
			},
			want: []string{"15:04:05", "allow", "read", "read_file", "ran"},
		},
		{
			name: "denied exec shows blocked and reason",
			entry: policy.Entry{
				Time: "2026-07-09T15:04:06Z", Tool: "shell",
				Class: tool.ClassExec, Decision: policy.Deny, Reason: "policy denies exec",
				Allowed: false,
			},
			want: []string{"deny", "shell", "blocked", "policy denies exec"},
		},
		{
			name: "tainted approved write",
			entry: policy.Entry{
				Time: "2026-07-09T15:04:07Z", Tool: "write_file",
				Class: tool.ClassWrite, Decision: policy.Ask, Approved: &allowed,
				Allowed: true, Tainted: true,
			},
			want: []string{"ask", "write_file", "ran", "tainted"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renderEntry(c.entry)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("render = %q, missing %q", got, w)
				}
			}
		})
	}
}

func TestRenderLinePassesThroughGarbage(t *testing.T) {
	if got := renderLine("not json"); got != "not json" {
		t.Errorf("garbage line = %q, want passthrough", got)
	}
}
