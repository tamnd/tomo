# Tool schema determinism

## Question

Do independent tomo processes serialize tools in the same order with exactly the same schema bytes?

## Method

Build the real `tomo` command once and launch it as separate operating system processes against one recording OpenAI-compatible endpoint.
Give every process the same workspace, provider, policy, memory index, and installed skill.
Capture the raw `tools` array from the first request made by each process.
Require exact byte identity across twelve deterministic processes and require the ordered names to match tomo's assembled registry.
Also compare every retry or extra request made within a process so provider retries cannot hide a serialization change.
Repeat the same proof across three separate processes that each call a real free Zen model.

```sh
go test -run ToolSchema -v ./labs/review/r2/04_tool_schema_determinism
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r2/04_tool_schema_determinism
```

## Verdict

Passed on 2026-07-21 with `north-mini-code-free`.
Three separate live tomo processes each returned `TOOL_SCHEMA_LIVE_OK` and serialized the same 5,353 tool bytes.
Twelve deterministic processes each returned `TOOL_SCHEMA_FAKE_OK` and serialized the same 5,349 tool bytes.
Both arms preserved the exact order `bash`, `read`, `grep`, `write`, `edit`, `fetch`, `plan`, `memory_read`, and `skill_read`.
The byte count differs between the separate test invocations because each temporary workspace path is embedded in the bash tool description, while every process inside one invocation shares the same path and exact bytes.
