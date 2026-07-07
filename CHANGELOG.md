# Changelog

All notable changes to tomo are recorded here.

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
