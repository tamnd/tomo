# Changelog

All notable changes to tomo are recorded here.

## Unreleased

## v0.2.11

A finish-side guard for the oi engine: a fix that turns its own reproduction green
but breaks tests that were passing before the turn is not done.

### Added

- The oi engine gains a regression guard, armed with `TOMO_OI_REGRESS=1`. Before the
  loop it records the project's currently-passing tests, and a turn that edited the
  tree cannot end while any of those now fails: at each finish attempt it re-runs the
  suite and refuses the ending, naming the broken tests, when the fix regressed any of
  them. It is the signal the reproduction gate does not give, which only proves the
  reported bug is fixed, not that working behavior still works. It runs the project's
  own existing tests, never a hidden grading suite, and ignores any authored scratch
  reproduction, so it stays general and untailored. Off by default so it can be A/B'd,
  and bounded by a firing cap so a run still terminates.

## v0.2.10

A canonical trace store, and the harness authoring a task's reproduction test.

### Changed

- The trace store is now one append-only JSONL rollout per run instead of a WAL-mode
  SQLite database. One process owns one file and only appends, so there is no lock and
  no sidecar to lose, which is what an earlier container copy did to the SQLite `-wal`,
  publishing empty traces. The file is the publishable artifact directly. `traces
  export [RUN_ID]` (no argument exports the newest run) writes it as STS.

### Added

- The oi engine gains a test-authoring sub-flow, armed with `TOMO_OI_TESTGEN=1`. Before
  the loop it makes one call over the issue text alone, writes the returned test into
  the workspace, smoke-checks that it collects, and arms the reproduction gate so the
  model must make the already-failing test pass. It targets the failure the example
  gate left open, where a cheap model told to write the test skips it and drifts to the
  easiest item. It reads the issue alone and names no file or symbol from it, and
  supersedes the example gate rather than stacking with it.

## v0.2.9

Sharper tools for real codebases: a code search primitive, a surgical file edit,
and hard caps on how much any tool can hand back, so the agent works a large repo
without drowning its own context.

### Added

- A `grep` tool searches the codebase. With a pattern it returns matching lines as
  `path:line: text`; with no pattern it lists files, optionally filtered by a
  glob, which is how you find files by name. It runs ripgrep when it is on the
  path (the speed and match quality every serious agent gets from rg) and falls
  back to a bounded pure-Go walk when rg is absent, so a plain single-binary
  install still searches. Results are capped.
- An `edit` tool changes a file in place by replacing an exact, unique snippet, so
  a one-line fix in a thousand-line file is one small edit instead of rewriting
  the whole file. A non-unique or missing match is a clear error, not a silent
  misedit.
- A `--yolo` flag runs tomo fully autonomous: every action the policy gate would
  stop to ask about is auto-approved, which is how an agent works unattended in a
  sandbox. It is off by default, so tomo stays safe out of the box, and it never
  overrides an explicit `deny` rule, only the asks. `--dangerously-skip-permissions`
  is a hidden alias for anyone reaching for the name Claude Code uses. Without it,
  a headless `tomo -p` run still declines what it cannot ask a human about.
- The oi engine gains an issue-example gate, armed with `TOMO_OI_EXAMPLES=1`. Before
  the loop it makes one focused call that distills the task's issue into a checklist
  of the concrete cases the issue itself states, then injects them as required
  red-to-green targets and holds the finish to a real red-to-green. It converts a
  vague multi-behavior issue into a per-case contract the model cannot satisfy by
  fixing only the one case it happened to try. It reads the issue text alone, never
  the workspace or any hidden test, and is off by default so it can be A/B'd.
- The oi engine gains a convergence directive, appended with `TOMO_OI_FOCUS=1`. When
  an issue lists several things to change, it tells the model they are graded
  independently and to land them one at a time, writing each item's focused test,
  fixing it in the source, and confirming green before the next, so every stretch of
  work leaves a committed change behind. It targets an empty-patch failure measured
  at both ends of the model range, where a strong model at maximum reasoning effort
  and a cheap free model both read a long "port these N items" issue as one surface,
  explored every item, and finished with no source edit at all. It names no file or
  symbol from the issue and is off by default, meant to pair with the issue-example
  gate.

### Changed

- The `shell`, `read_file`, and `write_file` tools are renamed `bash`, `read`, and
  `write`, matching the names models are trained on so the agent reaches for them
  more reliably. Update any per-tool `rules` in your config to the new names.
- Every tool's output is now capped. `bash` clamps combined output to a head and
  tail with the middle elided, `read` takes `offset` and `limit` to page through a
  large file and truncates over-long lines, and a search never floods the context.
  A tool that trims its output says so and points at how to see the rest.
- The agent works in fewer tool calls. The system prompt now tells it not to
  re-read a file it just wrote or repeat a check that already passed, and to end
  the turn once its test or build is green, so a solved task stops looping and
  spends fewer model calls.
- A turn now runs its tool-use rounds until the model ends the turn on its own.
  A fixed round limit cut long multi-step tasks off before they finished; a turn
  ends when the model stops calling tools or an upstream error breaks the loop.
- A turn that stops making progress no longer spins forever. The agent watches
  for rounds where every tool call repeats one it already made; a burst of those
  earns one nudge to stop retrying and make the fix, and if the repetition keeps
  up the turn ends. It bounds the spin, not the length: any new distinct call
  resets the count, so a long task doing real work is never cut short. This is
  what keeps a weaker model from looping the same command dozens of times when a
  broken environment makes every attempt fail the same way.
- The system prompt now tells the agent to diagnose before it changes code: read
  the failing test or the real error first, make the smallest fix for that cause,
  and do not build one reproduction after another or rerun a command whose output
  it already has.
- The `agent` config section is gone, along with its two knobs. The `max_tokens`
  and `max_turns` settings were removed: the model runs to its own output limit
  and the turn to its own end, which is how the agent already behaved with them
  unset. A config that still carries an `agent` block loads fine, the section is
  ignored.

### Fixed

- A reply cut off at the model's output limit no longer ends the turn. A model
  that reasons at length can spend its whole output budget on hidden reasoning
  and come back with a length stop and no tool call; the turn used to read that
  as the model choosing to stop and gave up having done nothing. It now nudges
  the model to take a concrete step and keeps going, a bounded number of times so
  a model that never stops reasoning cannot spin forever.
- A transient upstream failure no longer sinks a whole turn. When a gateway drops
  a completion mid-stream, it either sends an error payload as an SSE data line or
  cuts the body short; both used to unmarshal to an empty, successful-looking
  reply, so the turn ended having done nothing and the whole task had to be redone.
  The agent now surfaces the mid-stream error, marks dropped streams, 5xx, 429,
  and network errors as transient, and retries the model call a few times with a
  short backoff. A permanent error, a 400 or a 401, still fails on the first try.

## v0.2.4

Finishes the job on the first try more often: the agent runs the tests before it
claims to be done, and a reasoning model is no longer cut off mid-answer by a low
token cap.

### Changed

- The agent verifies its own code before ending a turn. When it writes or changes
  code the system prompt now tells it to run the project's tests or build, read
  the output, and keep fixing until they pass. A clean exit with no error output
  used to be enough for it to call a task done, which meant a wrong first draft
  shipped untested; now only a passing run ends the turn.
- `max_tokens` is left unset by default instead of capped at 8192. A fixed cap is
  a guess, and on a reasoning model a low one gets the reply or the tool call
  truncated once the hidden reasoning has spent the budget. Zero now means send no
  cap, so the model runs to its own limit, which is what the OpenAI-style
  providers already do when the field is omitted. The Anthropic provider, whose
  API requires the field, falls back to 32000, the largest output every current
  Claude model accepts. Set `max_tokens` in the config to put a ceiling back.

### Fixed

- A Gemini-backed run keeps the real tool_call id end to end. Gemini's protocol
  carries function names, not ids, so the wire used to mint its own id for each
  call, which an upstream that only honors the ids it issued would reject and the
  tool loop would stall. The id now rides through Gemini's `functionCall.id` and
  `functionResponse.id` fields when the client sends one, and is synthesized only
  when it is absent, so the continuation carries the id the server expects.

## v0.2.3

Makes a multi-step job cheap: tomo plans it in context instead of paying to
rebuild state in a fresh context per step.

### Added

- A `plan` builtin tool. It is a scratchpad the model calls to write a short
  checklist and update each step's status as it works, all inside one turn. It
  has no side effects, so it runs without a gate prompt, and it rejects an empty
  plan or more than one step in progress. The system prompt tells the model to
  reach for it when a task has three or more distinct steps, then do the steps
  in the same turn.

### Changed

- `tomo -p` runs a multi-step job in one turn now. The model plans it in context
  with the `plan` tool rather than escalating to the step-per-fresh-context
  orchestrator, which keeps the whole job in one growing conversation. On a
  storefront benchmark this cut a job from forty model round-trips and 94k
  tokens to eight round-trips and 24k tokens, still planning and still passing.
  The explicit `tomo plan run` command still drives the orchestrator for callers
  who want steps run as isolated workers.
- The system prompt moved into `pkg/agent/prompts/system.md`, an editable
  Markdown template embedded at build time and rendered with the run's
  workspace, persona, date, and memory and skills indexes, so the prose is
  easier to change than string concatenation was.

### Fixed

- A Gemini-backed run no longer crashes on a text-only turn or a non-object tool
  argument. Function-call arguments are kept a JSON object, wrapping a bare value
  as `{"value": ...}`, and a candidate's parts are kept non-empty with an empty
  text part when a turn would otherwise send none. The other wires are untouched.

## v0.2.2

### Added

- A configurable `workspace`: the directory the `read_file`, `write_file`, and
  `shell` tools are rooted at. A relative path the agent writes lands there, the
  shell runs there, and the system prompt tells the agent where it is so it stops
  guessing a home directory. Absolute paths and a `~` prefix still work as
  before. Defaults to the directory tomo was launched from, so nothing changes
  for an existing setup. Set it top-level or per worker.
- `tomo -p "<prompt>"`, a root `-p`/`--prompt` flag that runs one prompt
  non-interactively and exits, for scripts and pipelines. It reuses the chat
  build helpers, so the policy gate, memory, and toolset are identical to the
  REPL, and it takes the whole prompt as one turn rather than fragmenting a
  multi-line prompt across several.
- `tomo doctor`, a preflight command that checks the provider key, the data
  dir, and any configured channels, and exits non-zero on the first failure so
  a broken setup is named instead of guessed at.
- `tomo watch`, which tails the audit log and prints one line per gate
  decision, the read side of the policy gate.
- `tomo version`, which prints the version, commit, build date, Go toolchain,
  and target platform. A release binary still carries these from goreleaser's
  stamp; a binary from `go install` or `go build` now falls back to the build
  info the Go toolchain embeds in every binary, recovering the commit and date
  from the module's pseudo-version when no VCS setting is present. `tomo
  --version` folds the same detail into one line instead of a bare "dev".
- `pkg/wire`, stdlib-only translators between chat-completions and the
  OpenAI Responses, Anthropic Messages, and Google Gemini wires: request
  bytes in, chat completions bytes out, and the reply translated back,
  streaming included. Lets a chat-only backend sit behind agents that speak a
  different wire, with no knowledge of HTTP or the upstream connection.

### Fixed

- A model that ends a tool call with truncated, unparseable JSON arguments no
  longer wedges the run. The bad block is coerced to an empty object when the
  streamed call is assembled and again when history is flattened onto the
  wire, so the tool returns a plain error and the model can retry instead of
  every following turn repeating the same 400.
- The audit log's scrubber now redacts secret-shaped values in a tool's input
  before writing the entry, so a key a tool carried never lands in a log a
  person or `tomo watch` reads.
- Listen-address loopback classification now parses and range-checks the host
  instead of string-matching it, closing a gap where a trailing-dot hostname
  slipped past a comparable guard.

### Removed

- The generated line-count table in `AUDITING.md` and the CI job that enforced a
  budget on it. The doc still maps the security-critical packages; the enforced
  count was churn without a payoff. `scripts/audit-loc.sh` is gone.

## v0.2.1

Makes tomo easier to audit, safer when it runs a shell, and easier to extend
with a new channel.

### Added

- An opt-in OS-level sandbox for the shell tool. The gate still decides whether
  a command runs; the sandbox bounds what it can touch once it does. Four modes
  from `restricted` to `dev`, set top-level or per worker, enforced by Seatbelt
  on macOS and namespaces on Linux. Off by default, so a plain install stays one
  binary with nothing to configure, and the CGO-free build is unchanged.
- A channel driver registry modeled on the standard library's `database/sql`.
  Channels dispatch by name, so the core imports no specific channel and the
  config is a plain map keyed by driver name. `tomo channel list` shows the
  built-in drivers, and `tomo channel scaffold <name>` generates a starter
  adapter that compiles.
- `AUDITING.md`, which names the security-critical packages and what each
  enforces, backed by a generated line-count table and a CI budget so the
  "how much is there to read" number stays honest.

### Changed

- The `channels` config is now a map keyed by driver name. iMessage no longer
  takes an `enabled` flag; the presence of its block turns it on, like the rest.

### Security

- Built with Go 1.26.5, which carries the `crypto/tls` fix for GO-2026-5856.

## v0.2.0

Fills in the last install channel and sharpens the provider docs.

### Added

- The apt and dnf channel is live. A tagged release now refreshes the shared
  Linux package repository, so `sudo apt install tomo` and `sudo dnf install tomo`
  work alongside the archives, Homebrew cask, Scoop manifest, and container image.

### Docs

- The configuration guide now covers hosted OpenAI-compatible gateways, not just
  local servers, and spells out the `provider/model` naming: the provider key is
  yours to pick, the model after the slash is whatever the endpoint serves. Any
  endpoint that speaks the OpenAI chat completions dialect and returns `tool_calls`
  drives the full agent loop.
- Quick start points at that provider block for anyone starting on a gateway or a
  local model rather than Anthropic.

## v0.1.0

First release.

- Single CGO-free Go binary: chat from the terminal, or `tomo serve` for a
  local web chat plus Telegram, Discord, Slack, and iMessage behind allow-lists.
- A policy gate under the model: reads and network run, writes and code execution
  ask first, and a successful network fetch taints the session so writes and exec
  escalate back to ask.
- Markdown memory a curator maintains across conversations, markdown skills, named
  worker specialists, scheduling, and voice in and out on the local machine.
- Model-agnostic: Anthropic native, or any OpenAI chat completions endpoint.
- Ships as archives, deb/rpm/apk, a Homebrew cask, a Scoop manifest, and a signed
  multi-arch container image, with cosign signatures and SBOMs.
