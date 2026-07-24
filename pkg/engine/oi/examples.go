package oi

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/tamnd/tomo/pkg/provider"
)

// The issue-example gate (spec 2109, the lever after the reproduction gate) is a
// pre-loop sub-flow, not a prompt line. The reproduction gate proved that asking
// a model to verify harder does not lift a task whose fix it cannot write: on a
// vague multi-behavior issue the model edits the right functions and still gets
// the semantics wrong, because "reproduce the bug" leaves it to decide what the
// bug even is, and it picks one case and declares victory. The audited runs that
// stalled on dynaconf-1225 all had the graded functions in context and edited
// them; what they never did was turn each concrete behavior the issue spells out
// into its own executable target.
//
// This sub-flow does that turning mechanically. Before the loop, it makes one
// focused model call over the ISSUE TEXT ALONE and asks it to enumerate the
// distinct concrete cases the issue describes, each as a one-line input ->
// expected-result target. That checklist is then injected as required targets:
// the model must write a red-then-green test for every enumerated case, not one
// of its choosing, and the reproduction gate holds the finish to a real
// red-to-green. The value is the enumeration: it converts the issue's prose into
// an explicit, per-case contract the model cannot satisfy by fixing the one case
// it happened to try.
//
// It stays on the right side of the no-tailoring line by construction. The
// extraction call reads only the issue the task shipped with, never the
// workspace's own tests and never the hidden grading suite, and it names no file
// or symbol the harness supplied. The cases it surfaces are the issue author's
// own examples, which the model was always free to read; the sub-flow only makes
// the model treat each as a required target instead of an optional one. Armed
// opt-in (TOMO_OI_EXAMPLES=1) so it can be A/B'd, and it arms the reproduction
// gate with itself so the red-to-green discipline backs the checklist.

// examplesMax bounds how many cases the checklist carries. A handful of concrete
// targets focuses the model; a long list reads as noise and a runaway extraction
// cannot bloat the turn.
const examplesMax = 8

// examplesExtractSystem is the whole instruction for the extraction call. It is
// deliberately narrow: read the issue, list the concrete behaviors it describes
// as testable one-liners, invent nothing, and touch no file. The output shape is
// fixed so the parser can lift the lines without the model's prose around them.
const examplesExtractSystem = "You read a bug report or feature request and distill it into a checklist of concrete, testable behaviors, nothing else. " +
	"List only behaviors the text itself describes with a concrete input and an expected result; do not invent cases the text does not state, and do not generalize beyond it. " +
	"Write each as a single line: a concrete input or setup, then the expected result, terse enough to become one test. " +
	"Output only the list, one behavior per line, each line starting with '- '. No preamble, no code, no explanation. " +
	"If the text describes no concrete testable behavior, output nothing."

// examplesDirective heads the injected checklist. It frames the enumerated cases
// as required red-to-green targets, each of them, and ties off with the same
// scratch-file discipline the reproduction gate uses so the tests do not touch
// the graded suite.
const examplesDirective = "The issue describes these specific behaviors, each a concrete case with an input and an expected result:\n\n%s\n\n" +
	"Every one of these is a required target, not a suggestion. Before you finish, write a small focused test for EACH case, in a scratch file so it does not touch the project's own test suite, run it against the current code and see it FAIL (red, which proves it captures the reported behavior), then make your fix and run it again until it PASSES (green). " +
	"A fix that turns one of these cases green while leaving another red is not done. Do not end the turn until every case above has gone from red to green."

// extractExamples makes the one focused extraction call and returns the concrete
// cases the issue states, at most examplesMax of them. It fails soft: any call
// error, an empty reply, or a reply with no list lines returns nil, so a run that
// cannot extract pays nothing and the loop is unchanged. It never reads anything
// but the issue text handed to it.
func (e *Engine) extractExamples(ctx context.Context, issue string) []string {
	issue = strings.TrimSpace(issue)
	if issue == "" {
		return nil
	}
	req := provider.Request{
		Model:    e.Model,
		System:   examplesExtractSystem,
		Messages: []provider.Message{provider.UserText(issue)},
	}
	// No sink: the extraction is internal plumbing, not part of the visible turn.
	resp, err := e.stream(ctx, req, nil)
	if err != nil || resp == nil {
		return nil
	}
	return parseExampleLines(assistantText(resp.Blocks))
}

// exampleLine matches a checklist bullet the extraction call was asked to emit:
// a line beginning with a dash or an asterisk, or a numbered "1." item. The
// captured group is the case text with the marker stripped.
var exampleLine = regexp.MustCompile(`^\s*(?:[-*]|\d+[.)])\s+(.*\S)\s*$`)

// parseExampleLines lifts the case lines out of the extraction reply, drops empty
// and duplicate cases, and caps the count. Anything that is not a list line is
// ignored, so a stray sentence of prose the model added does not become a target.
func parseExampleLines(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, ln := range strings.Split(text, "\n") {
		m := exampleLine.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		c := strings.TrimSpace(m[1])
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
		if len(out) >= examplesMax {
			break
		}
	}
	return out
}

// examplesMessage renders the checklist into the injected directive, or returns
// the empty string when there are no cases so the caller adds nothing.
func examplesMessage(cases []string) string {
	if len(cases) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range cases {
		b.WriteString("- ")
		b.WriteString(c)
		b.WriteByte('\n')
	}
	return fmt.Sprintf(examplesDirective, strings.TrimRight(b.String(), "\n"))
}
