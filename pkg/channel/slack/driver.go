package slack

import (
	"fmt"

	"github.com/tamnd/tomo/pkg/channel"
)

func init() { channel.Register("slack", driver{}) }

// driver opens a Slack channel from its config block.
type driver struct{}

// Open reads the app token (which opens the socket) and the bot token (which
// posts messages), plus the allowed channel ids. The app token is the one
// that must be present to run at all.
func (driver) Open(s channel.Settings) (channel.Channel, error) {
	app := s.String("app_token")
	if app == "" {
		return nil, fmt.Errorf("slack: app_token is required")
	}
	return &Slack{AppToken: app, BotToken: s.String("bot_token"), Allow: s.Strings("allow_channels")}, nil
}
