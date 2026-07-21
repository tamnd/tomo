# Persisted history remains unchanged

## Question

Does send-time compaction alter the full conversation history stored in tomo's named-session ledger?

## Method

Build the real `tomo` command and run a named terminal session with a 5,000-token send budget and a two-result verbatim tail.
Use one real model turn to read four large files in sequence and capture every complete provider request.
Reload the named session from the real `tomo.db` ledger after the process exits.
Find the first persisted tool result and require its exact JSON string to appear in an early provider request.
Require the final provider request to contain a `read archive-1.txt` re-fetch stub and not the full first result.
Require the persisted result to remain byte-identical to the early wire value, retain every sentinel, and contain no elision marker.
Use a deterministic scripted provider for CI and repeat the same CLI, compaction, persistence, and reload path with a real free Zen model.

```sh
go test -run PersistedHistoryUnchanged -v ./labs/review/r3/04_persisted_history_unchanged
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r3/04_persisted_history_unchanged
```

## Verdict

Record the date, model, request count, wire sizes, elision evidence, persisted message count, and exact-result comparison only after the full live test passes.

## Recorded result

On 2026-07-21, `north-mini-code-free` completed the four-read chain in five provider requests and returned `PERSISTENCE_LIVE_OK`.
The early request was 24,799 bytes and contained the exact JSON string for the 15,759-byte first tool result.
The final compacted request was 41,646 bytes, contained the `read archive-1.txt` re-fetch stub, and did not contain that full result.
After the process exited, the named session reloaded ten messages from `tomo.db`.
The persisted first result remained byte-identical to the early wire value, retained its unique sentinel, and contained no send-time elision marker.
