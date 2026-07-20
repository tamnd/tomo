package oi

import (
	"context"
	"sort"
	"strings"

	"github.com/tamnd/tomo/pkg/sandbox"
)

// The finish guard catches the turn-ending failure that hurts a code-as-action
// model most: it inspects the code, works out the fix, describes it in prose, and
// then ends the turn without ever writing the change. The stop condition, a reply
// with no runnable block means the model is done, fires on that reply and the turn
// ends having touched nothing. north-mini-code-free did exactly this on
// gitingest-94: four read-only shell blocks to find and read the file, then a
// reply with no runnable action, the one-line fix left unwritten.
//
// The guard mirrors the default engine's no-edit discipline. When the model wants
// to end but the workspace holds no change since the turn began, it nudges once to
// actually apply the fix instead of ending on nothing done. The signal is git: a
// worktree unchanged from the turn's start means nothing was written. It fires at
// most once a turn, and when the workspace is not a git worktree it does not fire
// at all, so a model that legitimately finished pays no extra round.

// noEditNudge fires when the model ran code this turn but ends with a clean
// worktree: it explored and quit without applying a fix.
const noEditNudge = "You are ending the turn, but you have not changed any file yet, so the issue is still unresolved. " +
	"Reading the code and describing the fix is not the fix. Write the edit into the source in a runnable block, then run the test that exercises it and confirm it passes."

// hallucinatedNudge fires when the model ran no code at all this turn yet its
// reply reads like it was acting on the code: a weak model in a code-as-action
// harness sometimes role-plays the whole session in prose without ever emitting a
// runnable block. It comes in two shapes. One claims the work is done ("the
// factory has been updated", "verified with the full test suite"). The other
// narrates a tool session that never ran ("I'll inspect the files", "the shell
// output is unavailable in this session, so I'll..."), inventing empty command
// output and then giving up. Both end with a clean worktree having touched
// nothing. The nudge tells it flatly that narration does not act and demands the
// real block.
const hallucinatedNudge = "No code block ran this turn, so nothing you described actually happened: no file was read, edited, or tested, and the working tree is unchanged. " +
	"If a command seemed to produce no output, that is because it never ran. In this session the only thing that acts is a fenced ```python or ```shell block that actually executes. Stop narrating the work as if it were done and emit the real block."

// looksLikeActing reports whether an assistant reply reads like the model was
// acting on the code, or claiming to, rather than merely answering a question. It
// is the signal that separates a model that hallucinated its tool use (talks like
// it inspected, edited, ran, or finished, but emitted no runnable block) from a
// model that only answered and never pretended to act. It is consulted solely on
// a turn that ended with no runnable block, so an announced action followed by an
// actual code block never reaches it; the only cost of a false positive is one
// extra nudge, while the false negative it prevents is a silent non-solve that
// ends on nothing done. The markers cover both hallucination shapes: past-tense
// completion claims, first-person intent to inspect or edit or run, and
// references to command output that could only exist if something had executed.
func looksLikeActing(text string) bool {
	t := strings.ToLower(text)
	for _, m := range actingMarkers {
		if strings.Contains(t, m) {
			return true
		}
	}
	return false
}

var actingMarkers = []string{
	// Past-tense completion claims: the model says the work is already done.
	"has been updated", "have been updated", "has been applied", "have been applied",
	"has been implemented", "have been implemented", "has been modified", "have been modified",
	"the change is applied", "the change is in place", "the edit is applied", "the fix is applied",
	"the minimal change is applied", "the change has been made",
	"i updated", "i edited", "i applied", "i implemented", "i modified", "i changed the",
	"i've updated", "i've edited", "i've applied", "i've implemented", "i've modified",
	"i have updated", "i have edited", "i have applied", "i have implemented",
	"updated `", "edited `", "modified `",
	"implemented the", "applied the fix", "applied the change", "made the change",
	"verified with", "verification passed", "the tests pass", "tests passed",
	"i ran the", "ran the test", "ran the focused", "ran the full", "the full test suite",
	// First-person intent to act on the code: announced but, on this branch, never
	// backed by a runnable block.
	"i'll inspect", "i will inspect", "i'll run", "i will run", "i'll use python",
	"i'll use the", "i'm checking", "i am checking", "i'll locate", "i'll search",
	"i'll verify", "i'll make the", "i'll apply", "i'll edit", "i'll check",
	"i'm locating", "i am locating", "i'll update", "i will update",
	// References to command output that can only exist if something executed: the
	// model is inventing a tool session it never ran.
	"did not produce visible output", "no visible output", "output is unavailable",
	"shell output is unavailable", "produced no output", "stdout in this session",
	"the shell runner", "not display stdout", "output is not displayed",
	// First-person investigative intent stated in the present tense: the model says
	// it still needs to find, read, or change the code, which only makes sense mid
	// solve. A weaker model narrates the whole investigation this way ("I need to
	// find the file, let me look at the function") and then stops with a clean tree,
	// treating the plan as the deliverable. These are distinct from a bare diagnosis
	// noun like "the fix would be", which can be a genuine answer to a question and
	// is deliberately left out so an answer-only turn is not nudged.
	"i need to find", "i need to look", "i need to locate", "i need to edit",
	"i need to change", "i need to modify", "i need to check", "i need to open",
	"let me look", "let me find", "let me locate", "let me check", "let me examine",
	"let me search", "let me inspect", "let me open", "let me read",
	"we need to change", "we need to modify", "we need to edit",
	// A model that opens the solve by announcing where it will begin, then stops on
	// the plan without emitting the first block. hy3-free quit in one round on
	// gitingest-94 with "Let me start by exploring the repository structure", which
	// none of the verb-specific "let me look/find" markers above catch: the
	// exploration-preamble phrasings are their own investigative-intent shape.
	"let me start", "let me begin", "let me explore", "let me first",
	"i'll explore", "i will explore", "i'll start", "i will start",
	"i'll begin", "i will begin", "start by exploring", "begin by exploring",
	// An attempted tool call that yielded no runnable block: the model reached for a
	// structured tool in XML, but either named a tool that does not exist (a weak
	// model invents an "AgenticSearch") or wrote a shape the salvage could not read.
	// Since this branch runs only after every dialect and the fenceless salvage
	// found nothing, the presence of a tool-call skeleton means the model tried to
	// act and the action was lost, which is the hallucination the nudge corrects.
	// The opening tag is matched as a prefix so an attributed call and the plural
	// wrapper both count: deepseek-v4-flash-free quit in one round on <tool_calls>
	// with a <tool_call kind="text_write"> that carried only narration, which a
	// literal "<tool_call>" would have missed.
	"<tool_call", "<tool_name", "<parameter name", "<function=",
}

// The dropped-block guard catches a distinct code-as-action failure: the model
// tries to create a source file by pasting its contents in a fenced block tagged
// with that file's language (```go, ```rust, ```toml, ...). Only python and shell
// blocks execute, so such a block runs nothing and the file is never written; the
// reply then carries no runnable block and the turn would end with the file
// missing, which typically breaks the build (an undefined name the new file was
// meant to define). A weak model reaches for this constantly, since pasting a
// whole file reads like the natural way to "write" it. The guard nudges once that
// a file is created by a heredoc or python write, then loops so the model does it.
//
// It fires only when the sole blocks in the reply are non-runnable and at least
// one names a source or config language a model would only include to create a
// file; a plain ```text or ```diff or a bare prose fence does not trigger it. Like
// the other guards it fires at most once a turn, so the cost of a false positive
// (a model that showed a ```json sample as its answer) is a single extra round.

// droppedBlockNudge fires when the model's only block this turn was a source or
// config file pasted in a non-runnable fence, so nothing wrote it to disk.
const droppedBlockNudge = "You wrote a ```%s block, but only python and shell blocks run in this session, so that block executed nothing and wrote no file. " +
	"Pasting a file's contents in a language fence does not create the file. To create or edit %s, write it from a shell block with a heredoc (for example: cat > path/to/file <<'EOF' … EOF) or from a python block that opens the file and writes it, then run that block and confirm the file is on disk."

// fileBlockLangs are the fence tags a model uses when it pastes a whole source or
// config file expecting the block to save it. A tag here that reaches the finish
// path (no runnable block ran) is the dropped-file-write signal. Prose and
// data-display tags (text, markdown, diff, log) are deliberately absent: a model
// ending on one of those is formatting an answer, not attempting a file write.
var fileBlockLangs = map[string]bool{
	"go": true, "rust": true, "rs": true, "c": true, "cc": true, "cpp": true,
	"c++": true, "h": true, "hpp": true, "java": true, "kotlin": true, "kt": true,
	"scala": true, "swift": true, "ts": true, "typescript": true, "tsx": true,
	"js": true, "javascript": true, "jsx": true, "mjs": true, "cjs": true,
	"php": true, "ruby": true, "rb": true, "perl": true, "lua": true, "dart": true,
	"groovy": true, "json": true, "yaml": true, "yml": true, "toml": true,
	"ini": true, "cfg": true, "xml": true, "html": true, "htm": true, "css": true,
	"scss": true, "sql": true, "proto": true, "dockerfile": true, "makefile": true,
	"cmake": true, "gradle": true, "csv": true,
}

// droppedFileBlock reports the language of the first non-runnable block in a reply
// that names a source or config file language, or ok=false when none does. It is
// consulted only after runnableBlocks found nothing to run, so a reply that also
// carried a real python or shell block never reaches it and an announced file
// pasted alongside a heredoc that actually writes it is not flagged.
func droppedFileBlock(all []block) (lang string, ok bool) {
	for _, b := range all {
		if _, runnable := language(b.Lang); runnable {
			continue
		}
		if fileBlockLangs[b.Lang] && strings.TrimSpace(b.Code) != "" {
			return b.Lang, true
		}
	}
	return "", false
}

// The verify-to-green guard mirrors cx's: it catches a coding turn that stops
// while its own last check is still red. In oi a check is a code block that runs
// the project's tests or build, and a coding turn is one that changed the
// worktree. When the model wants to end having edited but with its last verify
// block still failing, the guard nudges once that a red check is not a finish.
// Like the finish guard above it fires at most once a turn.

// verifyFailedNudge fires when the model ends a coding turn while its most recent
// verification block was still failing.
const verifyFailedNudge = "You are ending the turn, but the last test or build block you ran exited with an error, so a change you made this turn leaves the project failing. " +
	"Do not finish on a red check. Read that failure and fix its cause in the source: a missing import, an undefined name, a syntax error, or a real logic bug are yours to fix, not the test's. " +
	"Then run the same check again, and keep going until it passes. " +
	"If the failure is from parts of the suite that cannot run here (they need a service, a container, or credentials), do not fight them: run only the tests that exercise your change and confirm those are green."

// verifyMarkers are the command fragments that mark a code block as running the
// code to check it: a test runner, a build, a type or lint gate. The set matches
// cx's so the two engines read a verification the same way. The guard only needs
// to know "this was a check" so a failing one is not mistaken for an ordinary
// block (a grep that found nothing, an ls of a missing path) that legitimately
// exits non-zero.
var verifyMarkers = []string{
	"pytest", "py.test", "unittest", "tox", "nox",
	"py_compile", "assert ",
	"go test", "go build", "go vet", "golangci-lint",
	"npm test", "npm run test", "npm run build", "yarn test", "pnpm test",
	"jest", "vitest", "mocha", "tsc ", "eslint",
	"cargo test", "cargo build", "cargo check", "cargo clippy",
	"make test", "make check", "make ", "ctest", "cmake --build",
	"gradle", "mvn ", "rspec", "phpunit", "rake test",
	"mypy", "ruff", "flake8", "pylint",
}

// looksLikeVerify reports whether a code block is running the code to verify it
// rather than exploring or setting up. Matching is on a lowercased substring, so
// a shell block "cd x && python -m pytest -q tests/foo.py" and a python block
// that shells out to a test runner both count.
func looksLikeVerify(code string) bool {
	c := strings.ToLower(code)
	for _, m := range verifyMarkers {
		if strings.Contains(c, m) {
			return true
		}
	}
	return false
}

// worktreeState returns a content-sensitive workspace state and whether it could
// be observed reliably. Git worktrees use porcelain status plus hashes for dirty
// paths; plain directories use a bounded filesystem fingerprint.
func (e *Engine) worktreeState(ctx context.Context) (state string, tracked bool) {
	box := e.Box
	if box == nil {
		box, _ = sandbox.New("none", e.Workspace)
	}
	if out, err := box.Run(ctx, []string{"git", "rev-parse", "--is-inside-work-tree"}); err != nil || strings.TrimSpace(out) != "true" {
		if e.Workspace == "" {
			return "", false
		}
		fingerprint, fingerprintErr := fingerprintWorkspace(e.Workspace)
		return fingerprint, fingerprintErr == nil
	}
	out, err := box.Run(ctx, []string{"git", "status", "--porcelain"})
	if err != nil {
		return "", false
	}
	// Porcelain records that an untracked file exists, but not its contents. A
	// task workspace often starts with an untracked starter file, so editing that
	// file leaves the raw status text unchanged. Fingerprint every dirty path to
	// distinguish those real edits; deleted paths retain their status-only state.
	paths := make([]string, 0, len(dirtyPaths(out)))
	for path := range dirtyPaths(out) {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var fingerprint strings.Builder
	fingerprint.WriteString(out)
	for _, path := range paths {
		hash, hashErr := box.Run(ctx, []string{"git", "hash-object", "--", path})
		if hashErr == nil {
			fingerprint.WriteByte(0)
			fingerprint.WriteString(path)
			fingerprint.WriteByte('=')
			fingerprint.WriteString(strings.TrimSpace(hash))
		}
	}
	return fingerprint.String(), true
}
