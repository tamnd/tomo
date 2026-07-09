---
title: "Quick start"
description: "From an empty terminal to a working agent: set a key, onboard, talk to tomo in the terminal, then open the local web chat."
weight: 30
---

This walks the first run end to end.
By the last step you have an agent you can talk to from the terminal and from a browser, all on your own machine.

## 1. Set a provider key

The starter config points at Anthropic and reads the key from your environment, so export it first:

```bash
export ANTHROPIC_API_KEY=sk-ant-...
```

Pointing at a local model or a hosted OpenAI-compatible gateway instead?
Set that provider's `base_url` in the config after the next step, and export whatever key it expects (or none).
See [configuration](/guides/configuration/) for a provider block you can copy.

## 2. Onboard

`tomo onboard` sets up `~/.tomo` and writes a starter `config.yaml`.
It creates the `memory` and `skills` directories beside it, and it will not overwrite a config you already have.

```bash
tomo onboard
```

```
wrote /home/you/.tomo/config.yaml

you're set. try:
  tomo doctor   # confirm everything is ready
  tomo chat     # then ask: what can you do?
```

If you have not set the key yet, onboard prints the exact `export` line to run first instead.
Open `~/.tomo/config.yaml` if you want to change the default model, adjust the policy, or wire up a channel.
The defaults are ready to go as they are.

## 3. Confirm it is ready

`tomo doctor` checks the config, the provider key, the data dir, and any channels, printing a line per check:

```bash
tomo doctor
```

```
✓ default provider: anthropic/claude-fable-5 ready
✓ data dir: /home/you/.tomo writable
✓ channels: [web (always on)]

all good. next: tomo chat
```

If a check fails it names the fix and exits non-zero, and `tomo serve` runs the same checks before it starts, so nothing gets half-configured.

## 4. Talk to it in the terminal

`tomo chat` is a streaming REPL against the configured model:

```bash
tomo chat
```

```
tomo · anthropic/claude-fable-5 · /new starts over, /exit leaves

you> what can you do on this machine?
tomo> I can read and fetch things freely, and I can write files or run
commands with your approval each time. Want me to show you the current
policy?

you>
```

Two REPL commands: `/new` clears the working context and starts over, and `/exit` leaves.

By default the conversation is not saved.
Pass `-s` (or `--session`) with a name to persist it in the ledger and pick up where you left off next time:

```bash
tomo chat -s daily
```

You can also override the model for a single run with `-m` (or `--model`), for example `tomo chat -m anthropic/claude-fable-5`.

## 5. Open the web chat

`tomo serve` runs tomo as a daemon.
The web chat is always on, bound to loopback by default, and any channels you configured start alongside it:

```bash
tomo serve
```

```
tomo serving on http://127.0.0.1:8765
  channel: web
```

Open http://127.0.0.1:8765 in a browser and you have the same agent in a chat window.
It listens only on your machine unless you change `--addr`.

## Where to go next

- Connect a chat app: the [channels guide](/guides/channels/) wires up Telegram, Discord, Slack, and iMessage, each with an allow-list.
- Understand the gate before you loosen it: the [policy and safety](/guides/policy-and-safety/) guide is the whole trust model, what runs, what asks, and what taint does.
- Browse the rest of the [guides](/guides/) for memory and skills, workers, scheduling, voice, and MCP.
