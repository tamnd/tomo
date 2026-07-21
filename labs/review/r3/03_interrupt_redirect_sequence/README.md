# Interrupt and redirect sequence

## Question

Can tomo read a specification and related code past a compaction budget, survive a real process interruption, accept a redirect, edit the target, and verify the result?

## Method

Build the real `tomo` command and run a named terminal session with a 6,000-token send budget and a two-result verbatim tail.
Read one large specification and three large related-code files, then hold the fifth provider request open after the budget has been exceeded.
Send the tomo process `SIGINT`, require the blocked provider request to observe cancellation, and wait for the partial turn to persist and exit cleanly.
Start a new tomo process on the same named session and send a redirect that requires the model to re-read the compacted specification before changing code.
Require an exact `edit` call, a real shell verification, the expected file content, and the final success marker.
Use a deterministic scripted provider for CI and repeat the same process, tool, persistence, cancellation, and redirect path with a real free Zen model.

```sh
go test -run InterruptRedirectSequence -v ./labs/review/r3/03_interrupt_redirect_sequence
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r3/03_interrupt_redirect_sequence
```

## Verdict

Record the date, model, phase request counts, interrupted request bytes, elision evidence, process result, edit, verification, and final marker only after the full live test passes.

## Recorded result

On 2026-07-21, `north-mini-code-free` completed the two-process sequence with five provider requests before interruption and five after the redirect.
The held fifth request was 47,075 bytes and contained the `read spec.md` re-fetch stub, proving the 6,000-token send budget had been exceeded and compaction had activated.
`SIGINT` canceled that provider request, the first tomo process persisted its partial turn, and the process exited cleanly.
The new process resumed the same named session, received `REDIRECT_AFTER_INTERRUPT_742`, and re-read `spec.md` before editing.
The model changed `ORIGINAL_SETTING=off` to `REQUIRED_SETTING=on`, ran the specified `grep -Fx` verification through bash, and returned `SEQUENCE_LIVE_OK`.
The audit log contained allowed `edit` and `bash` calls, and the final file contained only the required setting.
