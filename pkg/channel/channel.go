// Package channel is the front-door abstraction: every place a user can talk
// to tomo (the web UI, Telegram, and later Discord, Slack, iMessage) is a
// Channel. Adapters stay thin. They translate their platform's messages into
// an Inbound, render replies through a Reply, and answer approvals through an
// Approver. Everything above that (sessions, the agent turn, policy) is the
// router's job and is written once.
package channel

import (
	"context"

	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/provider"
)

// Caps declares what a channel can do, so the router can degrade gracefully:
// no Buttons means approvals fall back to a yes/no text reply, no Stream means
// the reply is sent once at the end instead of incrementally.
type Caps struct {
	Media   bool // can carry images in and out
	Buttons bool // can render tap-to-approve controls
	Stream  bool // can update a message as text streams
}

// Clip is a speech attachment waiting to be transcribed. The router turns it
// into text before the turn runs; channels only have to carry the bytes and
// the container extension.
type Clip struct {
	Data []byte // the raw audio
	Ext  string // container extension, like ".ogg" or ".m4a"
}

// Inbound is one message arriving from a channel.
type Inbound struct {
	Chat   string           // conversation key within the channel
	User   string           // sender id, for allowlisting
	Text   string           // message text
	Images []provider.Block // any image blocks that came with it
	Audio  []Clip           // any voice notes that came with it, pre-transcription
}

// Message builds the provider message for an inbound, text first then images.
func (in Inbound) Message() provider.Message {
	m := provider.Message{Role: provider.RoleUser}
	if in.Text != "" {
		m.Blocks = append(m.Blocks, provider.Text(in.Text))
	}
	m.Blocks = append(m.Blocks, in.Images...)
	return m
}

// Reply is how the router talks back during one turn. Chunk carries streamed
// assistant text, Notice carries out-of-band status (tool activity, errors),
// and Done finalizes the turn.
type Reply interface {
	Chunk(text string)
	Notice(text string)
	Done()
}

// Exchange bundles one inbound message with the channel-side handles the
// router needs to answer it.
type Exchange struct {
	Channel  string
	In       Inbound
	Reply    Reply
	Approver policy.Approver
}

// Handler processes one exchange to completion.
type Handler func(ctx context.Context, x Exchange)

// Channel receives messages until its context is cancelled.
type Channel interface {
	Name() string
	Caps() Caps
	Run(ctx context.Context, h Handler) error
}
