//go:build !darwin

// Package imessage is a no-op everywhere but macOS. The type exists so the
// serve command compiles on every platform; Run just reports that the channel
// is unavailable here.
package imessage

import (
	"context"
	"errors"
	"time"

	"github.com/tamnd/tomo/pkg/channel"
)

// IMessage is the non-macOS stub. Its fields mirror the darwin build so the
// serve wiring is identical across platforms.
type IMessage struct {
	Allow  []string
	DBPath string
	Poll   time.Duration
}

// Name implements channel.Channel.
func (m *IMessage) Name() string { return "imessage" }

// Caps implements channel.Channel.
func (m *IMessage) Caps() channel.Caps { return channel.Caps{} }

// Run reports that iMessage is macOS only.
func (m *IMessage) Run(context.Context, channel.Handler) error {
	return errors.New("imessage channel is only available on macOS")
}

// Post reports that iMessage is macOS only.
func (m *IMessage) Post(context.Context, string, string) error {
	return errors.New("imessage channel is only available on macOS")
}
