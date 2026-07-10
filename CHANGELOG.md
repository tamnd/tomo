# Changelog

All notable changes to tomo are recorded here.

## Unreleased

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
