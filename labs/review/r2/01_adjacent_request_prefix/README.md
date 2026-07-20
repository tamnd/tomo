# Adjacent request prefixes

## Question

How many leading serialized bytes remain identical between adjacent provider requests in a long multi-round model turn?

## Method

Build the real `tomo` command with an isolated workspace and a recording OpenAI-compatible endpoint.
Start with `chain-1.txt`, whose content names `chain-2.txt`, whose content names `chain-3.txt`, so the model can discover only one next read at a time.
Give each file a substantial deterministic payload so the conversation grows over three tool rounds.
Capture every complete JSON request body before returning or forwarding the streamed response.
Measure each adjacent pair using the raw bytes exactly as sent, without parsing, canonicalizing, projecting, or removing dynamic messages.
Report both the common-prefix byte count and its percentage of the shorter request.
Use a deterministic scripted provider for CI and the same chain with a real free Zen model for the live arm.

```sh
go test -run Adjacent -v ./labs/review/r2/01_adjacent_request_prefix
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r2/01_adjacent_request_prefix
```

## Verdict

Record the date, model, request count, request sizes, exact adjacent common-prefix bytes, ratios, and final marker only after the full live test passes.

## Recorded result

On 2026-07-21, `north-mini-code-free` completed the chained file task in six provider requests and returned `PREFIX_LIVE_OK`.
The serialized request sizes were 8,681, 25,790, 42,899, 60,008, 77,440, and 94,549 bytes.
Adjacent raw byte-identical prefixes were 3,797, 20,906, 38,015, 55,124, and 72,556 bytes.
Those prefixes were 43.74%, 81.06%, 88.62%, 91.86%, and 93.69% of the shorter adjacent request.
Every first divergence occurred where the newer request appended its next assistant tool call after the complete prior shared history.
The capture and measurements used the exact transmitted JSON bodies without normalization or field removal.
