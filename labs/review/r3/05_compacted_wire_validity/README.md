# Compacted tool history wire validity

## Question

Does compacted tool-call history remain structurally valid in every provider wire format supported by tomo?

## Method

Run the real Agent loop with production built-in read tools and send-time compaction through both supported provider serializers: OpenAI chat completions and Anthropic messages.
Read four large files in sequence so the first results are replaced by re-fetch stubs while their assistant tool calls remain in history.
Capture the final native request for each dialect and validate every result references an existing tool call, every tool call has a result, all IDs remain unique, and the expected compaction stub is present.
Require both serialized histories to reach a real free Zen model and complete the chain.
Use scripted chat responses for deterministic CI, while the Anthropic arm uses tomo's production messages translator only as a bridge to the same chat upstream.

```sh
go test -run CompactedWireValidity -v ./labs/review/r3/05_compacted_wire_validity
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r3/05_compacted_wire_validity
```

## Verdict

Record the date, model, dialects, request counts, final request sizes, tool-pair counts, compaction stubs, and final markers only after the full live test passes.

## Recorded result

On 2026-07-21, `north-mini-code-free` completed the compacted chain through both supported provider serializers.
The OpenAI arm made six provider requests, and its final 34,598-byte chat-completions body contained five unique tool calls with five correctly matched tool results.
The Anthropic arm made six provider requests, and its final 20,595-byte messages body contained five unique tool-use blocks with five correctly matched tool-result blocks.
The free model made one additional read in each arm, and the validator covered that observed pair instead of assuming only the four requested calls.
Both final requests contained the `read wire-1.txt` compaction stub, both native histories translated to a valid Zen request, and both runs returned `WIRE_VALID_LIVE_OK`.
