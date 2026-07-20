# Provider cache metrics

## Question

What request size, input-token, cache-read, cache-write, time-to-first-byte, and wall-time values does a real provider report during a growing tomo tool turn?

## Method

Build the real `tomo` command and route its OpenAI-compatible stream through a byte-preserving local relay.
Give the model three substantial files that each direct one following read, then require a final marker.
For every provider call, record the complete request byte count, the first response-body byte, response completion, and raw streamed usage object.
Parse cache counters with presence flags so a provider that omits cache writes is not reported as a measured zero.
Require one stable `prompt_cache_key` across the growing turn and require request bytes and input tokens to grow.
Use a delayed deterministic upstream for CI and repeat the full chain against a real free Zen model.

```sh
go test -run ProviderCacheMetrics -v ./labs/review/r2/05_provider_cache_metrics
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r2/05_provider_cache_metrics
```

## Verdict

Passed on 2026-07-21 with `north-mini-code-free` and ended with `CACHE_METRICS_LIVE_OK`.
The real turn made seven calls and retained one `prompt_cache_key` while request bytes and provider input tokens grew.
Zen returned both cache-read and cache-write fields on every call, and every reported value was zero.

| Call | Request bytes | Input tokens | Cache read | Cache write | First byte | Wall time |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 8,675 | 1,781 | 0 | 0 | 852 ms | 1,671 ms |
| 2 | 9,271 | 1,820 | 0 | 0 | 585 ms | 978 ms |
| 3 | 27,180 | 5,243 | 0 | 0 | 676 ms | 3,462 ms |
| 4 | 27,893 | 5,295 | 0 | 0 | 701 ms | 1,082 ms |
| 5 | 45,802 | 8,718 | 0 | 0 | 753 ms | 2,387 ms |
| 6 | 64,121 | 12,342 | 0 | 0 | 721 ms | 2,183 ms |
| 7 | 65,026 | 12,406 | 0 | 0 | 789 ms | 2,166 ms |

The deterministic arm measured four growing calls and proved the relay distinguishes a reported zero from an absent cache field.
