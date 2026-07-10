#!/usr/bin/env bash
# run.sh drives tomo end to end against a real, OpenAI-compatible model and
# checks that the three things this branch adds actually hold when a live model
# is in the loop, not just in a unit test:
#
#   1. the model can call the sandboxed shell tool and read back its output
#   2. the sandbox refuses a write outside the working tree, and the failure
#      surfaces to the model as a normal command error (no crash, no leak)
#   3. the channel driver registry lists its drivers and scaffolds a new one
#
# It needs a key for an OpenAI-compatible endpoint. The defaults target the
# OpenCode Zen free tier, whose deepseek model does tool calling:
#
#   export OPENCODE_API_KEY=...
#   eval/run.sh
#
# Point it elsewhere by overriding the env vars below. Nothing here runs in CI:
# it calls a real model over the network and is a manual DX check by design.
set -euo pipefail

cd "$(dirname "$0")/.."

: "${OPENCODE_API_KEY:?set OPENCODE_API_KEY (or override PROVIDER_* for another endpoint)}"
BASE_URL="${TOMO_EVAL_BASE_URL:-https://opencode.ai/zen/v1}"
MODEL="${TOMO_EVAL_MODEL:-opencode/deepseek-v4-flash-free}"

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# A config that lets every class run, so the eval exercises the sandbox wall
# rather than the policy gate. sandbox: standard is the layer under test.
cat >"$work/config.yaml" <<YAML
default_model: $MODEL
data_dir: $work/data
providers:
  opencode:
    type: openai
    api_key: \${OPENCODE_API_KEY}
    base_url: $BASE_URL
agent:
  max_tokens: 2000
  max_turns: 8
policy:
  read: allow
  net: allow
  write: allow
  exec: allow
sandbox: standard
YAML

echo "building tomo..."
go build -o "$work/tomo" ./cmd/tomo
tomo="$work/tomo"

echo
echo "eval 1: model calls the sandboxed shell tool and reads its output"
out1="$(printf 'Run the shell command: echo hello-from-tomo\nThen tell me exactly what it printed.\n/exit\n' \
  | "$tomo" chat --config "$work/config.yaml" 2>&1)"
echo "$out1" | grep -q 'hello-from-tomo' \
  && pass "shell ran under the sandbox and the model read back the output" \
  || { echo "$out1"; fail "model did not report the command output"; }

echo
echo "eval 2: sandbox denies a write outside the working tree"
# The standard sandbox writes the working tree and tmp on purpose, so the
# canary has to sit outside both. Home is the honest out-of-tree target: the
# write must fail there. Clean it up whether or not the eval leaks.
canary="$HOME/.tomo-eval-canary"
trap 'rm -rf "$work"; rm -f "$canary"' EXIT
rm -f "$canary"
out2="$(printf 'Run this shell command and report the exact result: printf x > %s && echo WROTE || echo BLOCKED\n/exit\n' "$canary" \
  | "$tomo" chat --config "$work/config.yaml" 2>&1)"
# The one thing that must be true: the file was never created.
if [ -f "$canary" ]; then
  echo "$out2"; fail "sandbox leaked: the out-of-tree file was created"
fi
echo "$out2" | grep -q 'BLOCKED' \
  && pass "kernel refused the write and the model saw a clean command failure" \
  || { echo "$out2"; fail "expected the model to report BLOCKED"; }

echo
echo "eval 3: channel driver registry lists and scaffolds"
list="$("$tomo" channel list)"
for want in web telegram discord slack imessage; do
  echo "$list" | grep -qx "$want" || { echo "$list"; fail "channel list missing '$want'"; }
done
pass "channel list reports every registered driver"

# Scaffold into a throwaway copy so the eval never dirties the tree.
cp -R . "$work/tree"
( cd "$work/tree" && "$tomo" channel scaffold matrix >/dev/null )
gen="$work/tree/pkg/channel/matrix/matrix.go"
[ -f "$gen" ] || fail "scaffold did not write the driver file"
( cd "$work/tree" && go build ./pkg/channel/matrix/... ) \
  && pass "scaffolded driver compiles" \
  || fail "scaffolded driver did not compile"

echo
echo "eval 4: the tools catalog and the model's self-knowledge agree"
# The catalog lists what tomo can do; the model is handed the same tools. So
# 'tools search file' and the model's own answer to "what can you use to read a
# file" must name the same builtin. This proves the catalog is not a separate
# list that can drift from what a turn actually loads.
search="$("$tomo" tools search file --config "$work/config.yaml")"
echo "$search" | grep -q 'read_file' \
  || { echo "$search"; fail "tools search file did not list read_file"; }
out4="$(printf 'Name the exact tool you would call to read a text file from disk. Reply with just the tool name.\n/exit\n' \
  | "$tomo" chat --config "$work/config.yaml" 2>&1)"
echo "$out4" | grep -q 'read_file' \
  && pass "the catalog and the model both name read_file" \
  || { echo "$out4"; fail "the model did not name the tool the catalog lists"; }

echo
echo "eval 5: attach mcp writes a config that still loads"
# One-step attach must produce a config the loader accepts, or it is worse than
# hand-editing. Attach a server, then prove the same binary can load the result.
acfg="$work/attach.yaml"
cp "$work/config.yaml" "$acfg"
"$tomo" tools attach mcp files --command mcp-server-filesystem --arg "$work" --config "$acfg" >/dev/null \
  || fail "attach mcp returned an error"
grep -q 'mcp-server-filesystem' "$acfg" \
  || { cat "$acfg"; fail "attach did not write the server block"; }
"$tomo" tools --config "$acfg" 2>&1 | grep -q 'mcp files' \
  && pass "attached server is loaded and its dial attempt is reported" \
  || { "$tomo" tools --config "$acfg"; fail "attached server not picked up by the catalog"; }

echo
printf '\033[32mall evals passed\033[0m\n'
