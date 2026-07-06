# tomo (友)

[![ci](https://github.com/tamnd/tomo/actions/workflows/ci.yml/badge.svg)](https://github.com/tamnd/tomo/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/tamnd/tomo)](https://github.com/tamnd/tomo/releases/latest)
[![Go Reference](https://pkg.go.dev/badge/github.com/tamnd/tomo.svg)](https://pkg.go.dev/github.com/tamnd/tomo)
[![Go Report Card](https://goreportcard.com/badge/github.com/tamnd/tomo)](https://goreportcard.com/report/github.com/tamnd/tomo)
[![License](https://img.shields.io/github/license/tamnd/tomo)](./LICENSE)

Your personal AI agent, one Go binary.

tomo (友, "companion") sits between your chat apps and a language model.
You text it on Telegram, Discord, Slack, or iMessage, or open the local web chat, and it remembers you across conversations and acts with real tools: run commands, read and write files, fetch pages, save memories, schedule work.
Every action passes a fail-closed policy gate first, and content pulled in from the outside taints the session so injected instructions cannot quietly reach your shell.

It runs as one static binary on your own hardware.
Your provider key, your history, and your memory stay on your machine, and the only thing that leaves is the model call you would have made anyway.

[Install](#install) • [Quick start](#quick-start) • [Models](#models) • [Docs](https://tomo.tamnd.com/) • [Safety](https://tomo.tamnd.com/guides/policy-and-safety/)

![tomo showing its command surface, then writing a starter config with onboard and starting the daemon with the local web chat](docs/static/demo.gif)

## Install

```sh
go install github.com/tamnd/tomo/cmd/tomo@latest
```

Release archives, Linux packages (deb, rpm, apk), and a container image at `ghcr.io/tamnd/tomo` ship with each tag.

## Quick start

```sh
tomo onboard   # writes ~/.tomo/config.yaml and walks you through a provider
tomo chat      # talk from the terminal
tomo serve     # web chat on localhost plus every configured channel
```

## Models

tomo is model-agnostic.
The Anthropic API is native, and anything speaking the OpenAI chat completions dialect works through `base_url`: a local llama.cpp or ollama, a LAN inference box, or any hosted gateway.

```yaml
default_model: anthropic/claude-fable-5
providers:
  anthropic:
    type: anthropic
    api_key: ${ANTHROPIC_API_KEY}
  local:
    type: openai
    base_url: http://gamingpc:8000/v1
    api_key: ${LOCAL_API_KEY}
```

A model is named `provider/model`, so the `local` provider above serves models like `local/qwen2.5-coder`.
Set `default_model` to any of them, or override it per worker.

## What it does

- **Speaks through the chat apps you already use.** The local web chat is always on; Telegram, Discord, Slack, and iMessage start when you configure them, each with an allow-list.
- **Acts with real tools, behind a gate.** Reads and network run, writes and code execution ask, and anything you have not allowed is declined. The moment a turn pulls in untrusted content, the session is tainted and writes and execution escalate to ask.
- **Remembers you across conversations.** A markdown memory tomo reads and writes itself, with a curator that reflects after substantial turns and stamps each note with where it came from.
- **Gets better at your workflows.** It follows skills you write and drafts new ones from workflows it sees you repeat. Installing a skill is always your call.
- **Works on its own when you want.** Scheduled prompts and a heartbeat pick up standing work on a cadence and report back only when there is something worth saying.
- **Runs many agents when one is not enough.** Named workers with their own persona, model, policy, and memory, routed by `@name` or channel, and one can hand a task to another.
- **Speaks and listens.** Optional local voice: whisper transcribes voice notes in, piper speaks replies back, all on your machine.
- **Talks to other tools over MCP.** Attach MCP servers to extend the toolset, or serve tomo's own tools to Claude Code and other clients.

## Design in one breath

A gateway daemon owns sessions and a sqlite ledger.
Channel adapters are thin: they turn platform messages into events and render replies, nothing else.
Tools are typed and classified (read, net, write, exec), and a policy engine decides allow, ask, or deny per call, with approvals answered right in the channel you are talking on.
Memory is plain markdown you can read and edit yourself.

Full documentation lives at [tomo.tamnd.com](https://tomo.tamnd.com/), including the [policy and safety](https://tomo.tamnd.com/guides/policy-and-safety/) model in full.

## License

MIT
