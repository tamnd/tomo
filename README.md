# tomo (友)

Your personal AI agent, one Go binary.

tomo sits between your chat apps and a language model.
You text it on Telegram or open the local web chat, it remembers you across conversations, and it can act: run commands, fetch pages, save memories, schedule work.
Every action passes a fail-closed policy gate first, and content pulled in from the outside world taints the session so injected instructions cannot quietly reach your shell.

Early days.
The core runtime, the policy engine, and the first channels are here; more channels, skills, voice, and MCP are on the way.

## Install

```sh
go install github.com/tamnd/tomo/cmd/tomo@latest
```

## Quick start

```sh
tomo onboard   # writes ~/.tomo/config.yaml and walks you through a provider
tomo chat      # talk from the terminal
tomo serve     # web chat on localhost plus every configured channel
```

## Models

tomo is model-agnostic.
The Anthropic API is native, and anything speaking the OpenAI chat completions dialect works through `base_url`: a local llama.cpp or ollama, a LAN inference box, or any hosted gateway.

```yaml
default_model: anthropic/claude-fable-5
providers:
  anthropic:
    type: anthropic
    api_key: ${ANTHROPIC_API_KEY}
  local:
    type: openai
    base_url: http://gamingpc:8000/v1
    model_prefix: local
```

## Design in one breath

A gateway daemon owns sessions and a sqlite ledger.
Channel adapters (web chat, Telegram, more coming) are thin: they turn platform messages into events and render replies, nothing else.
Tools are typed and classified (read, net, write, exec), and a policy engine decides allow, ask, or deny per call, with approvals answered right in the channel you are talking on.
Memory is plain markdown you can read and edit yourself.

## License

MIT
