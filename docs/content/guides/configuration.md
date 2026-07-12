---
title: "Configuration"
description: "A tour of ~/.tomo/config.yaml: the default model, providers for Anthropic and OpenAI-dialect servers, ${VAR} env expansion, the agent knobs, the policy gate, the data directory, and pointers to every section documented in its own guide."
weight: 80
---

Everything tomo needs lives in one file, `~/.tomo/config.yaml`, written by `tomo onboard`.
This guide walks the top-level keys.
The channels, workers, voice, MCP, and heartbeat sections each have their own guide; this is the frame around them.
For the exhaustive key list, see the [config file reference](/reference/config-file/).

## Env expansion

Any value may reference an environment variable with `${VAR}`, expanded when the config loads.
So a key never has to live in the file itself:

```yaml
providers:
  anthropic:
    api_key: ${ANTHROPIC_API_KEY}
```

An unset variable expands to empty, so export it before you run tomo.

## The default model

`default_model` is the `provider/model` tomo uses when nothing else is specified.
The name before the slash must match a provider key below.

```yaml
default_model: anthropic/claude-fable-5
```

Commands that take `--model` (or `-m`) override it per run.

## Providers

`providers` maps a name to a backend.
Two types are supported: `anthropic`, and `openai` for anything speaking the OpenAI chat completions dialect.
Point the OpenAI type at a local server or a hosted gateway with `base_url`.

```yaml
providers:
  anthropic:
    type: anthropic
    api_key: ${ANTHROPIC_API_KEY}

  local:
    type: openai
    base_url: http://gamingpc:8000/v1
    api_key: ${LOCAL_API_KEY}

  gateway:
    type: openai
    base_url: https://your-gateway.example/v1
    api_key: ${GATEWAY_API_KEY}
```

Each provider takes `type`, `api_key`, and, for the OpenAI dialect, `base_url`.
The provider key is yours to choose; the model after the slash is whatever name the endpoint serves.
So `local/some-model` routes to the local server and `gateway/some-model` to the hosted one, with no code change.
Any endpoint that speaks the OpenAI chat completions dialect and returns `tool_calls` drives the full agent loop, so a self-hosted model and a hosted gateway configure the same way.

## Agent

`agent` holds the loop knobs:

```yaml
agent:
  max_turns: 24
```

- `max_tokens` caps a single model response. It is unset by default, so the model runs to its own limit; set it to put a ceiling back. A low cap truncates the answer or the tool call on a reasoning model, which spends part of the budget on hidden reasoning before the visible reply.
- `max_turns` caps the tool-use rounds in one turn before tomo stops, defaulting to 24.

## Policy

`policy` is the gate every tool call passes.
The starter config carries the safe posture: reads and network run, writes and code execution ask.

```yaml
policy:
  read: allow
  net: allow
  write: ask
  exec: ask
  rules:
    # bash: deny
    # write: allow
```

This is the shortest possible summary.
The [policy and safety](/guides/policy-and-safety/) guide is the full trust model: capability classes, per-tool rules, the taint escalation that blunts prompt injection, and how unattended runs fail closed.

## The rest, in their own guides

The remaining sections are documented where they belong:

- `channels` opens the front doors: web chat, Telegram, Discord, Slack, iMessage. See [channels](/guides/channels/).
- `workers` defines named specialists with their own persona, model, policy, and memory. See [workers](/guides/workers/).
- `voice` turns on transcription and spoken replies. See [voice](/guides/voice/).
- `mcp` attaches Model Context Protocol servers. See [MCP](/guides/mcp/).
- `heartbeat` runs tomo on a cadence against a checklist. See [scheduling](/guides/scheduling/).

## The data directory

`data_dir` is where tomo keeps its state, defaulting to `~/.tomo`.

```yaml
data_dir: ~/.tomo
```

Under it live:

- `config.yaml`, this file.
- `memory/`, the markdown memory with its `MEMORY.md` index and topic files.
- `skills/` and `skill-drafts/`, installed skills and the curator's proposals.
- `tomo.db`, the session ledger and scheduled jobs.
- `audit.log`, one line per policy decision.
- `workers/<name>/`, a subtree per specialist with its own memory and skills.
- `HEARTBEAT.md`, the default heartbeat checklist, when you enable it.

Because memory and skills are plain files, you can read, edit, and grep your agent's state like anything else on disk.

For the complete key list, keep the [config file reference](/reference/config-file/) alongside this guide.
