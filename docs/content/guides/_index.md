---
title: "Guides"
linkTitle: "Guides"
description: "Task-oriented walkthroughs for running tomo: the channels it answers on, named workers, memory and skills, scheduling and the heartbeat, voice, MCP in both directions, the config file, and the policy gate that sits under all of it."
weight: 15
featured: true
---

Each guide covers one job you actually do with tomo, grounded in the real config keys and commands.
They assume you have worked through the [quick start](/getting-started/quick-start/) and have a `~/.tomo/config.yaml` from `tomo onboard`.

- [Channels](/guides/channels/) are the front doors `tomo serve` opens: the local web chat plus Telegram, Discord, Slack, and iMessage, each with an allow-list, and `/session` to carry one conversation across them.
- [Workers](/guides/workers/) are named specialists with their own persona, model, policy, and memory, routed by `@name` or a channel binding, able to hand a task to a colleague.
- [Memory and skills](/guides/memory-and-skills/) cover the markdown memory tomo reads and writes itself, the curator that reflects after substantial turns, and the skills you install to teach it a workflow.
- [Scheduling](/guides/scheduling/) is how tomo works on its own: `tomo cron` jobs and the heartbeat, both of which fail closed when no one is watching.
- [Voice](/guides/voice/) runs speech both ways on your machine with whisper and piper, so no audio leaves the box.
- [MCP](/guides/mcp/) works in two directions: attaching MCP servers so their tools join tomo's, and serving tomo's own tools to other clients with `tomo mcp`.
- [Configuration](/guides/configuration/) is a tour of `~/.tomo/config.yaml`, from providers and the agent knobs to the data directory.
- [Provider data boundary](/guides/provider-data-boundary/) lists exactly what each model request sends and what remains local.
- [Policy and safety](/guides/policy-and-safety/) is the whole trust model: the gate every tool call passes, capability classes, taint, and what tomo will never do by default.

Read [policy and safety](/guides/policy-and-safety/) first if you care about what runs, what asks, and what taint does.
Everything else here builds on that gate.
