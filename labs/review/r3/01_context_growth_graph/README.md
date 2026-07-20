# Long-turn context growth

## Question

How does the serialized provider request grow by round when one real model turn alternates between large file reads and large command outputs?

## Method

Build the real `tomo` command and run it against an isolated workspace and a recording OpenAI-compatible endpoint.
Ask the model to follow a six-round chain containing three large file reads and two large shell outputs.
Place a unique sentinel in every tool result and assert that later provider requests contain all five sentinels.
Capture every complete JSON request body before forwarding it to Zen, measure its exact byte length, and print an ASCII graph with the change from the prior round.
Require request bytes to grow on every round because this experiment uses the default uncompacted conversation path.
Use a deterministic scripted provider for CI and repeat the same tool chain with a real free Zen model for the live arm.

```sh
go test -run ContextGrowth -v ./labs/review/r3/01_context_growth_graph
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r3/01_context_growth_graph
```

## Verdict

Record the date, model, exact request sizes, round deltas, sentinel checks, and final marker only after the full live test passes.

## Recorded result

On 2026-07-21, `north-mini-code-free` completed the chained task in six provider requests and returned `CONTEXT_GROWTH_LIVE_OK`.
The exact serialized request sizes were 8,687, 26,852, 54,014, 72,179, 99,341, and 117,814 bytes.
The changes from the prior round were 18,165, 27,162, 18,165, 27,162, and 18,473 bytes.
The first and third tool results were large file reads, while the second and fourth were large real shell outputs produced by `tomo`'s bash tool.
Each of the five unique sentinels appeared in the next provider request, proving that every tool result entered model history.
Every request grew, and the final request was 13.56 times the first request's size.
