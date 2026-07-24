package oi

import (
	"strings"
	"testing"
)

// TestParseExampleLines covers the checklist parser: it lifts dash, asterisk, and
// numbered list items, strips the marker, drops empties and duplicates, ignores
// prose that is not a list line, and caps the count at examplesMax.
func TestParseExampleLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "dash bullets",
			in:   "- env PRODUCTION,quiet,OTHER loads three envs, current_env stays default\n- module dummyc.dummyc_module loads via dotted path",
			want: []string{"env PRODUCTION,quiet,OTHER loads three envs, current_env stays default", "module dummyc.dummyc_module loads via dotted path"},
		},
		{
			name: "mixed markers and prose",
			in:   "Here are the cases:\n* first case does X\n1. second case does Y\n2) third case does Z\nsome trailing prose",
			want: []string{"first case does X", "second case does Y", "third case does Z"},
		},
		{
			name: "drops empty and duplicate",
			in:   "- dup case\n- \n- dup case\n-   real second  ",
			want: []string{"dup case", "real second"},
		},
		{
			name: "no list lines",
			in:   "the issue describes no concrete testable behavior",
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseExampleLines(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %d cases %v, want %d %v", len(got), got, len(c.want), c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("case %d: got %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestParseExampleLinesCap verifies the count is bounded at examplesMax so a
// runaway extraction cannot bloat the injected checklist.
func TestParseExampleLinesCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < examplesMax+5; i++ {
		b.WriteString("- case ")
		b.WriteByte(byte('a' + i))
		b.WriteByte('\n')
	}
	got := parseExampleLines(b.String())
	if len(got) != examplesMax {
		t.Fatalf("got %d cases, want cap of %d", len(got), examplesMax)
	}
}

// TestExamplesMessage checks the rendered directive: empty in, empty out; and a
// non-empty list is embedded with the required-target framing and each case.
func TestExamplesMessage(t *testing.T) {
	if msg := examplesMessage(nil); msg != "" {
		t.Fatalf("empty cases should render empty, got %q", msg)
	}
	msg := examplesMessage([]string{"alpha does X", "beta does Y"})
	for _, want := range []string{"- alpha does X", "- beta does Y", "required target", "red", "green"} {
		if !strings.Contains(msg, want) {
			t.Errorf("rendered directive missing %q:\n%s", want, msg)
		}
	}
}
