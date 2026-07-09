package webchat

import (
	"github.com/tamnd/tomo/pkg/channel"
)

func init() { channel.Register("web", driver{}) }

// driver opens the web chat from its config block. Unlike the other channels
// the web chat is always on, so serve opens it directly rather than waiting for
// a config block; the driver exists so it goes through the same registry and so
// an addr set in config is honored.
type driver struct{}

// Open reads the listen address, defaulting to loopback so the web chat is
// private until a user chooses to expose it.
func (driver) Open(s channel.Settings) (channel.Channel, error) {
	addr := s.String("addr")
	if addr == "" {
		addr = "127.0.0.1:8765"
	}
	return &WebChat{Addr: addr}, nil
}
