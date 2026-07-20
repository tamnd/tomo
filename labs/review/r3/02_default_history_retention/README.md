# Default history retention

## Question

Does tomo's default behavior preserve the original specification, user constraints, approval record, and early file reads throughout a long model turn?

## Method

Build the real `tomo` command and run one isolated turn with all `TOMO_COMPACT_*` environment variables removed.
Put unique values in the user specification, a user constraint, the first large file read, and an approved shell result.
Configure shell execution as `ask`, feed one explicit `y` approval through stdin, and verify the durable audit entry records an allowed ask decision with `approved: true`.
Continue through three more large file reads so the original material is old context by the final provider request.
Capture every complete provider request and require the final body to retain every value without an elision marker.
Require the real model to return all four values in its final answer.
Use a deterministic scripted provider for CI and repeat the same chain with a real free Zen model for the live arm.

```sh
go test -run DefaultHistoryRetention -v ./labs/review/r3/02_default_history_retention
TOMO_REVIEW_LIVE=1 OPENCODE_API_KEY=... TOMO_REVIEW_MODEL=north-mini-code-free go test -run Live -v ./labs/review/r3/02_default_history_retention
```

## Verdict

Record the date, model, request count, final request size, retained values, approval record, and final answer only after the full live test passes.

## Recorded result

On 2026-07-21, `north-mini-code-free` completed the retention chain in six provider requests.
The final serialized request was 75,598 bytes and contained `amber-quartz-731`, `never-use-write-tool-284`, `cedar-compass-619`, and `violet-anchor-452` without a compaction elision marker.
The model returned all four values in the required final answer and never called the write tool.
The shell call stopped for terminal approval before execution, then the audit log retained `decision: ask`, `approved: true`, and `allowed: true` after the turn completed.
The lab removed all three `TOMO_COMPACT_*` variables from the child environment, so these observations cover the public default behavior.
