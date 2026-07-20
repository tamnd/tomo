# Cache prefix A/B

## Question

How does a stable static prefix compare with an otherwise identical prefix whose final system-prompt bytes change on every call?

## Method

Run tomo's production agent loop and OpenAI provider through a measuring HTTP transport.
Give both arms the same model, system prompt, read tool, three files, and user request.
Require both arms to read the same three files in order and return the same final marker.
Normalize the outgoing JSON in both arms.
Leave the control system message unchanged and append a fixed-width Markdown comment to the end of the treatment system message with a new value on every call.
Record raw request bytes, adjacent common-prefix bytes, provider input tokens, cache reads, cache writes, first-byte time, and wall time.
Use a deterministic cache-aware upstream for CI and repeat both arms against a real free Zen model.

```sh
go test -run CachePrefixAB -v ./labs/review/r2/06_cache_prefix_ab
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r2/06_cache_prefix_ab
```

## Verdict

Passed on 2026-07-21 with `north-mini-code-free`.
Both live arms scored 4 of 4 by reading `ab-1.txt`, `ab-2.txt`, and `ab-3.txt` in order before returning `CACHE_AB_OK`.

| Arm | Calls | Quality | Request bytes | Input tokens | Cache reads | Cache writes | First-byte total | Wall total | Adjacent prefix total |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Stable | 4 | 4/4 | 82,895 | 19,105 | 0 | 0 | 2,754 ms | 15,265 ms | 44,379 bytes |
| Late-changing | 4 | 4/4 | 83,055 | 19,141 | 0 | 0 | 3,586 ms | 9,340 ms | 10,818 bytes |

The late-changing arm added 160 request bytes and 36 input tokens while retaining only 24.4 percent as many adjacent prefix bytes.
Zen reported zero cache reads and writes for both arms, so this run cannot attribute a cache-hit advantage to the stable control.
The treatment had a slower aggregate first byte but a faster aggregate wall time, which shows the latency noise of one live run and is not evidence that prefix mutation improves latency.
The deterministic arm held quality equal while the stable control received 8,400 cache-read tokens and retained 44,301 adjacent prefix bytes versus zero cache-read tokens and 10,809 prefix bytes for the treatment.
