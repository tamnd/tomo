# Changelog

All notable changes to tomo are recorded here.

## Unreleased

### Added

- A configurable `workspace`: the directory the `read_file`, `write_file`, and
  `shell` tools are rooted at. A relative path the agent writes lands there, the
  shell runs there, and the system prompt tells the agent where it is so it stops
  guessing a home directory. Absolute paths and a `~` prefix still work as
  before. Defaults to the directory tomo was launched from, so nothing changes
  for an existing setup. Set it top-level or per worker.

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
