# Cache routing compatibility

## Question

Does tomo's OpenAI-compatible cache routing hint work with an accepting real provider and remain usable with a strict provider that rejects unknown fields?

## Method

Send two production OpenAI-provider calls with the same system prompt and tool schema to a real free Zen model.
Capture the exact outgoing `prompt_cache_key`, require it to be non-empty and stable, and require both real responses to complete.
Run the same provider against a strict deterministic endpoint that returns a 400 response whenever the routing field is present.
Require tomo to surface that rejection without retries.
Set `TOMO_PROMPT_CACHE_KEY_OFF`, repeat the call, require the field to be absent, and require the strict endpoint to accept it.

```sh
go test -run CacheRouting -v ./labs/review/r2/07_cache_routing_compat
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r2/07_cache_routing_compat
```

## Verdict

Passed on 2026-07-21 with `north-mini-code-free`.
Zen accepted `tomo-f89d86715b02c0f3` on two real calls with the same static system prompt and tool schema.
The calls returned `CACHE_ROUTING_LIVE_ONE` and `CACHE_ROUTING_LIVE_TWO`.
The strict endpoint rejected the keyed request once with a 400 unknown-field response, and tomo did not retry that non-transient error.
With `TOMO_PROMPT_CACHE_KEY_OFF=1`, tomo omitted the field and the same strict endpoint returned `STRICT_ROUTING_OK`.
