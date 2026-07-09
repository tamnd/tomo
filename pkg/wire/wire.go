// Package wire translates between the three foreign chat wires a coding agent
// might speak (OpenAI Responses, Anthropic Messages, Google Gemini) and the
// OpenAI Chat Completions wire that most hosted models actually serve.
//
// The problem it solves: a single shared model may only expose
// /v1/chat/completions, yet the tools you want to run against it each speak a
// different wire. codex posts to /v1/responses, Claude Code posts to
// /v1/messages, gemini-cli posts to /v1beta/models/{model}:generateContent. A
// proxy sitting in front of the model can accept any of those, translate the
// request down to chat completions, forward it, and translate the reply back,
// so every tool runs against the one model on equal footing.
//
// This package is the pure translation half of that proxy. It has no knowledge
// of HTTP, trace recording, or the upstream connection: request bytes in, chat
// bytes out; chat bytes in, foreign bytes (or a foreign event stream) out. The
// caller owns reading the request, forwarding to the upstream, recording
// usage, and writing the response. That keeps the translators reusable and
// unit-testable without a live model.
//
// Every translator targets the shapes a coding agent actually sends: messages,
// tool calls, and tool results. Multimodal parts beyond text are flattened to
// text rather than dropped, so nothing goes silently missing.
package wire

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ChatPathOf maps a foreign wire's path onto its /chat/completions sibling,
// keeping any version prefix intact, so /v1/responses and /v1/messages both
// become /v1/chat/completions. A path it does not recognize passes through.
func ChatPathOf(p string) string {
	for _, suf := range []string{"/responses", "/messages"} {
		if base, ok := strings.CutSuffix(p, suf); ok {
			return base + "/chat/completions"
		}
	}
	return p
}

// chatRole maps a foreign message role onto one chat completions accepts. The
// newer OpenAI wire tags harness instructions with the developer role, which
// the plain chat endpoint rejects, so it folds into system; an empty role
// defaults to user.
func chatRole(role string) string {
	switch role {
	case "", "user":
		return "user"
	case "developer", "system":
		return "system"
	default:
		return role
	}
}

// contentText flattens a content value into a plain string. It handles a bare
// string, an array of typed parts (input_text/output_text/text), and falls
// back to the raw JSON so nothing is silently dropped.
func contentText(raw json.RawMessage) string {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return ""
	}
	if t[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	if t[0] == '[' {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &parts) == nil {
			var b strings.Builder
			for _, p := range parts {
				b.WriteString(p.Text)
			}
			return b.String()
		}
	}
	return string(raw)
}

// chatChunk is the slice of a chat streaming chunk the stream translators read.
// All three foreign event streams are built from this one shape.
type chatChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
}

// rawInput parses a tool_call argument string back into a JSON value for a
// wire that carries tool arguments as an object rather than a string, falling
// back to an empty object.
func rawInput(args string) any {
	if strings.TrimSpace(args) == "" {
		return map[string]any{}
	}
	var v any
	if json.Unmarshal([]byte(args), &v) == nil {
		return v
	}
	return map[string]any{}
}
