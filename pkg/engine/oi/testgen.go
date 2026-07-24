package oi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/fence"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/sandbox"
)

// testgen (spec 2109, the lever after the issue-example gate) closes the gap that
// gate left open. The example gate enumerates the issue's concrete cases and asks
// the model to write a red-to-green test for each, but a cheap model swamped by a
// long multi-item issue skips the writing and goes straight to the easy edits,
// then has no failing test to verify against and drifts to whichever item is
// cheapest to touch. Measured on dynaconf-1225: a free model with the example gate
// and the convergence directive both armed shipped sixty-six lines that fixed the
// trivial cli one-liners and never touched the graded settings_loader slice, and
// wrote no test at all (experiment 0079). Telling a model to write the test first
// is not the same as it writing one. So the harness writes it.
//
// Before the loop, testgen makes one focused call over the ISSUE TEXT ALONE and
// asks for a complete runnable test file covering the concrete cases the issue
// states. It writes that file into the workspace itself, so a failing reproduction
// exists from round zero whether or not the model would have written one, then
// smoke-checks that the file at least collects: imports resolve and it parses. A
// file that cannot be collected is a broken test, useless as a target, so it is
// regenerated once with the collection error fed back; if it still cannot collect
// the sub-flow degrades to nothing rather than arm a red-to-green gate on a file
// that can never go green. When the file does collect, the model's job for the
// rest of the turn is only to make the already-failing tests pass, which anchors it
// on the graded behavior instead of the cheapest item, and the reproduction gate
// holds the finish to a real red-to-green against a test that is on disk from the
// start.
//
// It stays on the right side of the no-tailoring line the same way the example
// gate does. The authoring call reads only the issue the task shipped with, never
// the workspace's own tests and never the hidden grading suite, and names no file
// or symbol the harness supplied. The tests it writes are the issue author's own
// examples turned executable, which the model was always free to write itself; the
// sub-flow only guarantees they exist and are well-formed. Armed opt-in
// (TOMO_OI_TESTGEN=1) so it can be A/B'd, and it supersedes the example gate rather
// than stacking with it, so the run pays one authoring call, not two.
//
// The smoke check and the default filename lean on Python's pytest, which is what
// the current corpus runs; wording the authoring prompt for the project's own
// framework and generalizing the collect command to other runners is a follow-up.

// reproTestFile is where the authored reproduction lands in the workspace. It is a
// scratch file with a tomo-specific name so it cannot collide with the project's
// own suite, and the injected directive tells the model not to edit or delete it.
const reproTestFile = "test_tomo_repro.py"

// testgenSystem is the whole instruction for the authoring call. It asks for one
// self-contained, runnable test file covering the issue's concrete cases, output as
// a single fenced code block with nothing around it so the block can be lifted
// clean. It forbids inventing behavior the issue does not state and forbids reading
// or importing the project's own test suite, keeping the authored tests the issue
// author's own examples and off the tailoring line.
const testgenSystem = "You read a bug report or feature request and write ONE self-contained test file that reproduces every concrete behavior it describes, nothing else. " +
	"Cover each distinct case the text states with a concrete input and an expected result as its own test function; do not invent cases the text does not state. " +
	"Write standard pytest: plain `def test_...` functions with `assert`, importing only the project package under test and the standard library, never the project's own existing test files or fixtures. " +
	"Each test must assert the expected behavior so that it FAILS on the current buggy code and will PASS once the bug is fixed. " +
	"Output only the file, as a single fenced ```python code block, with no prose before or after it."

// testgenDirective is injected after the file is written. It tells the model the
// reproduction already exists, is failing because the bug is unfixed, and is the
// contract to satisfy: make every test in it pass by editing the project source,
// not the test file. It ties into the reproduction gate, which holds the finish
// until an executing check goes red to green.
const testgenDirective = "A reproduction test file for this issue has already been written to ./%[1]s. " +
	"It covers the concrete cases the issue describes and it currently FAILS, because the bug is not fixed yet. " +
	"Run it (`python -m pytest %[1]s -rA`), read which cases fail, then edit the PROJECT SOURCE until every test in it passes. " +
	"Do not edit, delete, or weaken ./%[1]s: if a test in it looks wrong, the fix belongs in the source, not the test. " +
	"You are not done until every test in ./%[1]s has gone from red to green."

// writeReproTests runs the authoring sub-flow: author a reproduction test file from
// the issue, write it into the workspace, smoke-check that it collects (with one
// regeneration on a collection error), and return the directive that points the
// model at it plus whether the reproduction gate should be armed. It fails soft:
// an empty issue, no workspace, a failed call, an unparsable reply, or a file that
// will not collect after one retry all return ("", false), so a run that cannot use
// the lever leaves the loop unchanged. It reads only the issue text handed to it.
func (e *Engine) writeReproTests(ctx context.Context, issue string, sink agent.Sink) (string, bool) {
	issue = strings.TrimSpace(issue)
	if issue == "" || e.Workspace == "" {
		return "", false
	}
	if !e.authorAndInstall(ctx, issue) {
		return "", false
	}
	if sink != nil {
		sink.Text("\n[testgen] wrote reproduction tests to " + reproTestFile + "\n")
	}
	return fmt.Sprintf(testgenDirective, reproTestFile), true
}

// authorAndInstall makes the authoring call for the given prompt, writes the
// returned file to the workspace reproduction path, and smoke-checks that it
// collects, regenerating once with the collector's error fed back. It returns
// whether a collectable reproduction is now on disk. It is the shared installer
// for the whole-issue test-authoring sub-flow and the per-item checklist
// decomposer, which differ only in the authoring prompt: one file, one path, one
// collect discipline. A file that will not collect after one retry is removed, so
// a caller that gets false is left with no scratch test and can leave the loop
// unchanged rather than arm a gate on a file that can never go green.
func (e *Engine) authorAndInstall(ctx context.Context, prompt string) bool {
	code := e.authorReproTests(ctx, prompt, "")
	if code == "" {
		return false
	}
	path := filepath.Join(e.Workspace, reproTestFile)
	if err := os.WriteFile(path, []byte(code), 0o644); err != nil {
		return false
	}
	if ok, collectErr := e.collectTest(ctx, reproTestFile); !ok {
		// A test that cannot even collect is malformed (a bad import, a syntax slip).
		// Regenerate once with the collector's own error fed back, then re-check.
		if code = e.authorReproTests(ctx, prompt, collectErr); code != "" {
			_ = os.WriteFile(path, []byte(code), 0o644)
		}
		if ok, _ := e.collectTest(ctx, reproTestFile); !ok {
			// Still broken: a red-to-green gate on a file that can never collect would
			// trap the model forever. Remove it and leave the loop unchanged.
			_ = os.Remove(path)
			return false
		}
	}
	return true
}

// authorReproTests makes one authoring call and returns the code of the first
// fenced block in the reply, or "" on any failure. When collectErr is non-empty it
// is the regeneration call, and the collector's error is appended so the model can
// fix what would not collect. No sink: the authoring is internal plumbing, not part
// of the visible turn.
func (e *Engine) authorReproTests(ctx context.Context, issue, collectErr string) string {
	prompt := issue
	if collectErr != "" {
		prompt = issue + "\n\nYour previous test file failed to collect with this error. Output the corrected file:\n\n" + collectErr
	}
	resp, err := e.stream(ctx, provider.Request{
		Model:    e.Model,
		System:   testgenSystem,
		Messages: []provider.Message{provider.UserText(prompt)},
	}, nil)
	if err != nil || resp == nil {
		return ""
	}
	return firstCodeBlock(assistantText(resp.Blocks))
}

// collectTest runs pytest in collect-only mode on the authored file: it imports and
// parses the tests without running them, so a well-formed file that merely fails
// its assertions still collects cleanly, while a bad import or a syntax error does
// not. It returns whether collection succeeded and the captured output for the
// regeneration prompt.
func (e *Engine) collectTest(ctx context.Context, name string) (bool, string) {
	box := e.Box
	if box == nil {
		box, _ = sandbox.New("none", e.Workspace)
	}
	out, err := box.Run(ctx, []string{"python3", "-m", "pytest", name, "--co", "-q"})
	return err == nil, clampOutput(out)
}

// firstCodeBlock lifts the code of the first non-empty fenced block out of a reply,
// which is the authored test file. The authoring prompt asks for exactly one
// ```python block, but a model may wrap prose around it or add a stray fence, so
// this takes the first block with real content and ignores the rest.
func firstCodeBlock(reply string) string {
	for _, b := range fence.ParseMarkdown(reply) {
		if strings.TrimSpace(b.Code) != "" {
			return b.Code
		}
	}
	return ""
}
