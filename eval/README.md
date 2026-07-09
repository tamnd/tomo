# eval

Live end-to-end checks for the features on this branch, run against a real
model. Unit tests prove each piece in isolation; this proves they hold when an
actual model is driving the tools, which is the only way to know the DX is real.

```sh
export OPENCODE_API_KEY=...      # any OpenAI-compatible endpoint works
eval/run.sh
```

By default it targets the OpenCode Zen free tier, whose deepseek model does
tool calling. Override `TOMO_EVAL_BASE_URL` and `TOMO_EVAL_MODEL` to point at
your own provider.

What it checks:

1. The model calls the sandboxed shell tool and reads back its output.
2. The sandbox refuses a write outside the working tree, the file never
   appears, and the failure reaches the model as a normal command error.
3. The channel driver registry lists its drivers and scaffolds a new one that
   compiles.

## features.sh

`run.sh` covers this branch's plumbing; `features.sh` walks the whole
user-facing experience the features doc promises, one use case at a time,
against the same live model.

```sh
export OPENCODE_API_KEY=...
eval/features.sh            # run every use case
eval/features.sh uc2 uc5    # run a subset by name
```

Each check asserts on a real effect, not on the model's exact words, since the
free tier is nondeterministic: a file that exists or does not, a canary that
never got written, a value recalled in a fresh process, a job written to the
ledger.

- **uc1** tomo answers as itself.
- **uc2** the model writes a file through its tools and reads it back.
- **uc3** a named session recalls a fact across two separate processes.
- **uc4** a memory written in one session is recalled in another.
- **uc5** the gate blocks a declined write and lets an approved one through.
- **uc6** fetched web content taints the session, so an injected exec
  escalates to ask and never runs.
- **uc7** `tomo watch` surfaces the gate's recorded decisions.
- **uc8** the web chat serves its page on loopback and streams a reply over SSE.
- **uc9** the agent schedules a follow-up and it lands in the ledger to run.

Voice needs whisper and piper binaries and a live Telegram needs a human chat,
so those are not driven here; the script says so rather than faking them.

Both scripts are manual checks. They call a real model over the network, so
neither runs in CI.
