---
title: "Workers"
description: "Named specialist workers with their own persona, model, policy, and memory. How routing picks a worker by @name or channel binding, and how one worker hands a self-contained task to a colleague, one level deep and gated by its own policy."
weight: 30
---

One agent is the default, and most deployments never need more.
When you want a specialist that answers some conversations in its own right, define it under `workers:` in the config.
Each worker gets its own persona, an optional model, its own policy, and its own memory, so nothing one learns leaks into another's prompt.

The default worker is tomo itself, and it needs no entry.
Add a `workers:` block and you have a small workforce that `tomo serve` routes between.

## What a worker carries

```yaml
workers:
  research:
    persona: You dig up sources and summarize. Cite what you find.
    model: anthropic/claude-fable-5
    policy:
      write: deny        # this one only reads and reports
    channels: ["slack:C0RESEARCH"]
```

- `persona` is extra system-prompt text that sets its role.
- `model` is a `provider/model` override; leave it empty to use the default model.
- `policy` is the worker's own gate, merged over the top-level [policy](/guides/policy-and-safety/): a field it sets wins, a field it leaves unset falls back to the default, and rules merge with the worker's winning.
- `channels` is a list of `channel:chat` keys whose messages route to this worker.

Each worker keeps its own memory subtree under `<data_dir>/workers/<name>/memory`, with its own [curator](/guides/memory-and-skills/) and its own skills.
So the `research` worker above never sees what the default worker remembered, and the reverse holds too.

The name `tomo` is reserved for the default worker; using it as a worker name is an error.

## How routing picks a worker

For each incoming message, tomo decides who handles it in this order:

1. An explicit `@name` prefix wins.
   Start a message with `@research ...` and it goes to `research`, with the prefix stripped before the model or the ledger sees it.
   An unknown `@name` falls through rather than erroring.
2. A `channel:chat` binding is next.
   If the chat this message arrived on is bound to a worker in its `channels` list, that worker handles it.
   A given `channel:chat` can only be bound to one worker.
3. Otherwise the default worker, tomo, takes it.

So a Slack channel bound to `research` routes there by default, but you can still reach the default worker in that same channel with an explicit prefix, and reach another colleague with theirs.

## Handoff

With more than one worker defined, each worker gets a `handoff` tool.
A worker uses it to hand a self-contained task to a colleague and get their answer back, when another worker is better suited to a piece of the work.

The handoff is one level deep on purpose:

- The colleague starts fresh and sees nothing of the calling conversation, so the handing worker must include everything the task needs in the message.
- The colleague runs one stateless turn and returns its reply as the tool result.
- The colleague's agent is built without a handoff tool of its own, so it cannot hand off again or loop back.
- The colleague runs with no one to approve an ask, so it is gated by its own policy, and anything that would need approval is declined, exactly like an [unattended run](/guides/policy-and-safety/).

A solo deployment has no colleagues, so there is no handoff tool at all.

## A worked example

Say you want a research specialist that only reads and reports, living in one Slack channel, while everything else stays with the default worker.

```yaml
policy:
  read: allow
  net: allow
  write: ask
  exec: ask

workers:
  research:
    persona: >
      You dig up sources and summarize what you find.
      Cite every claim. You do not write files or run commands.
    model: anthropic/claude-fable-5
    policy:
      write: deny
      exec: deny
    channels: ["slack:C0RESEARCH"]
```

Messages in Slack channel `C0RESEARCH` route to `research`, which inherits `read: allow` and `net: allow` from the top level but has `write` and `exec` denied outright, so it can fetch and summarize but never change the machine.
Anywhere else, the default worker answers.
If the default worker decides a task is better handled by `research`, it can hand it off; the delegate reports back and never gets to write, since its policy denies it and an unattended ask is declined anyway.
