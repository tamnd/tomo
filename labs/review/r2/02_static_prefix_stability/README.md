# Static prefix stability

## Question

Do the serialized system prompt and tool schemas remain byte-identical across transient retries, approved tool rounds, and an active turn crossing midnight?

## Method

Build the real `tomo` command with a recording OpenAI-compatible endpoint and a read policy set to `ask`.
Return one transient 500 response, then require the command to retry the exact request body.
Return a read tool call, feed `y` to the terminal approval prompt, and capture the next request containing the tool result.
Extract the raw serialized system-message content and raw `tools` array from every complete request body and require byte identity.
Run a real `agent.Agent` with a system prompt constructed at 23:59:59 and provider calls timestamped on opposite sides of midnight.
Require that direct agent turn to retain the exact system string and ordered tool definitions after midnight.
Use a deterministic scripted provider for CI and forward the same retry and approval scenario to a real free Zen model for the live arm.

```sh
go test -run 'Static|Midnight' -v ./labs/review/r2/02_static_prefix_stability
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r2/02_static_prefix_stability
```

## Verdict

Passed on 2026-07-21 with `north-mini-code-free`.
The live run captured three complete requests and received `STATIC_PREFIX_LIVE_OK` after the approved read result.
The first 500 response caused an exact byte-for-byte retry, and the terminal displayed the read approval prompt before accepting `y`.
All three live requests retained the same 3,583 serialized system bytes and 4,754 serialized tool bytes.
The deterministic command run retained the same 3,581 system bytes and 4,752 tool bytes across its retry and approved tool round.
The direct agent run observed provider calls at `2026-07-21T23:59:59Z` and `2026-07-22T00:00:01Z` while retaining byte-identical system and tool values.
