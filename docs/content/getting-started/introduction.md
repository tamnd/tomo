---
title: "Introduction"
description: "What tomo is, why a local-first agent keeps your keys and history on your machine, how the gate sits below the model, and when you would reach for it."
weight: 10
---

Most agent products run in someone else's cloud.
They hold your provider key, they see every message, and they log your conversations on a server you do not control.
tomo (友, "companion") takes the opposite stance.
It is a single Go binary you run yourself, and it puts a language model behind the chat apps you already use.

## Local-first, and why it matters

tomo runs on your machine.
Your provider key lives in your environment, your conversation history lives in a sqlite ledger under `~/.tomo`, and your memory is plain markdown in the same place.
None of it passes through a tomo service, because there is no tomo service.
The only thing that ever leaves the box is the model call itself, the same request you would have made talking to the provider directly.

That single fact changes the trust question.
You are not asking whether to trust a company with your data; you are running the agent next to your data and deciding, per call, what it may touch.

## The gate sits below the model

An agent that can run commands and fetch pages is useful and dangerous in the same breath.
The danger is not a malicious model, it is that a web page or a file the agent reads can carry instructions, and a naive agent follows them as readily as it follows you.

tomo's answer is a policy gate that every tool call passes through before it runs.
The gate returns allow, ask, or deny.
Reads and network calls run freely, writes and code execution stop to ask, and anything you have not allowed is declined.
The important part: the gate lives in tomo's own code, below the model, so a jailbroken prompt or an injected instruction still has to get past a decision the model does not control.

There is one more move that matters.
The moment a turn pulls in untrusted content from the web or a file, the session is tainted, and writes and execution escalate to ask even if you had allowed them.
So text fetched off a page cannot quietly drive your shell.

The gate decides whether a command runs; an optional OS-level sandbox decides how much it can touch once it does.
It is off by default, so a plain install stays one binary with nothing to set up, and you can confine a deployment or a single worker's shell when you want a command bounded to its working tree and off the network.
The [policy and safety](/guides/policy-and-safety/) guide is the whole trust model in depth: capability classes, per-tool rules, taint, the sandbox, and what tomo will never do by default.

## Model-agnostic

tomo is not tied to one provider.
The Anthropic API is native, and anything speaking the OpenAI chat-completions dialect works through a `base_url`: a local llama.cpp or ollama, a LAN inference box, or a hosted gateway.
You name the default model in the config and can override it per run with `--model`.

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

Provider values take `${VAR}` environment expansion, so your keys never have to sit in the file.

## When you would reach for it

- You want an assistant you text from Telegram, Discord, Slack, or iMessage, or from a local web chat, without handing your history to a third party.
- You want the agent to do real work on your machine (run commands, read and write files, fetch pages) but with a gate you set, not a vendor's defaults.
- You want it to remember you across conversations, in markdown you can read and edit yourself.
- You want to point it at a local or self-hosted model and keep the whole loop on your own hardware.

Next: [install tomo](/getting-started/installation/).
