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

This is a manual check. It calls a real model over the network, so it does not
run in CI.
