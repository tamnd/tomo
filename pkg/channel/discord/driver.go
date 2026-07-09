package discord

import (
	"fmt"

	"github.com/tamnd/tomo/pkg/channel"
)

func init() { channel.Register("discord", driver{}) }

// driver opens a Discord channel from its config block.
type driver struct{}

// Open reads the bot token and the allowed channel ids.
func (driver) Open(s channel.Settings) (channel.Channel, error) {
	token := s.String("token")
	if token == "" {
		return nil, fmt.Errorf("discord: token is required")
	}
	return &Discord{Token: token, Allow: s.Strings("allow_channels")}, nil
}
