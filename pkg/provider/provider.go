// Package provider defines the model-neutral message types and the streaming
// interface every model backend implements. The agent loop only ever sees
// these types; the Anthropic and OpenAI wire dialects stay inside their own
// files and translate at the edge.
package provider

import (
	"context"
	"encoding/json"
)

// Role is who a message is from. Tool results ride on user messages, the way
// both wire dialects expect.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockType enumerates the kinds of content a message can carry.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockImage      BlockType = "image"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

// Block is one piece of message content, a tagged union: Type says which
// fields mean anything. A plain struct beats an interface here because blocks
// round-trip through JSON for the session ledger.
type Block struct {
	Type BlockType `json:"type"`

	// BlockText.
	Text string `json:"text,omitempty"`

	// BlockImage: base64 payload plus its MIME type.
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`

	// BlockToolUse.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// BlockToolResult.
	ToolID  string `json:"tool_id,omitempty"`
	Content string `json:"content,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

// Text builds a text block.
func Text(s string) Block { return Block{Type: BlockText, Text: s} }

// Message is one conversation entry.
type Message struct {
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
}

// UserText builds the common case, a user message holding one text block.
func UserText(s string) Message {
	return Message{Role: RoleUser, Blocks: []Block{Text(s)}}
}

// Tool describes a callable tool the way the model sees it. Schema is a JSON
// schema for the input object.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Request is one model call.
type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []Tool
	MaxTokens int
}

// Usage counts tokens for one call.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Stop reasons, normalized across dialects.
const (
	StopEndTurn   = "end_turn"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
)

// Response is the fully assembled reply to one call.
type Response struct {
	Blocks     []Block
	StopReason string
	Usage      Usage
}

// EventType tags a streaming event.
type EventType int

const (
	// EventText carries a text delta.
	EventText EventType = iota
	// EventToolUse fires once per tool call, as soon as its name is known.
	EventToolUse
)

// Event is a streaming callback payload.
type Event struct {
	Type EventType
	Text string
	Name string
}

// Provider streams one model response. emit may be nil when the caller does
// not care about deltas; Stream always returns the complete response.
type Provider interface {
	Stream(ctx context.Context, req Request, emit func(Event)) (*Response, error)
}
