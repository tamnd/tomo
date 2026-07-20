# Anthropic cache breakpoints

## Question

Does tomo place Anthropic cache breakpoints on the static tool prefix and latest message while preserving real usage counters across stable and mutated prefixes?

## Method

Send production Anthropic-provider requests through tomo's Messages-to-Chat wire translator.
Forward the translated model calls to a real free Zen model.
Map Zen's raw cache-read and cache-write counters into the Anthropic start event before returning it to the production provider parser.
Require every raw Anthropic request to place one ephemeral breakpoint on the last tool and one on the last block of the latest message.
Require the extracted system-plus-tools prefix to remain exact for the stable pair and differ for the pair whose final system bytes change.
Compare normalized input, cache-read, and cache-write counters for all four calls.
Use a deterministic cache-aware upstream for CI and the real free model for the live arm.

```sh
go test -run AnthropicCache -v ./labs/review/r2/08_anthropic_cache_breakpoints
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r2/08_anthropic_cache_breakpoints
```

## Verdict

Passed on 2026-07-21 with `north-mini-code-free` behind tomo's Messages-to-Chat bridge.
All four production Anthropic requests placed exactly one ephemeral breakpoint on the last tool and one on the latest message block.
The two stable requests retained exact system-plus-tools bytes, while both final-system-byte mutations produced distinct static prefixes.
The real responses returned `ANTHROPIC_STABLE_ONE`, `ANTHROPIC_STABLE_TWO`, `ANTHROPIC_MUTATED_ONE`, and `ANTHROPIC_MUTATED_TWO`.
Zen reported 59 input tokens for each stable request and 67 for each mutated request.
Zen's real cache-read and cache-write counters were present and zero on all four calls.
The deterministic arm normalized 1,200 cache-read tokens on the second stable request, while both mutated requests reported zero reads and 600 writes.
