package telegram

import (
	"fmt"

	"github.com/tamnd/tomo/pkg/channel"
)

func init() { channel.Register("telegram", driver{}) }

// driver opens a Telegram channel from its config block.
type driver struct{}

// Open reads the bot token and the allowed chat ids. A missing token is an
// error rather than a silent off, since a telegram block in the config is a
// request to run it.
func (driver) Open(s channel.Settings) (channel.Channel, error) {
	token := s.String("token")
	if token == "" {
		return nil, fmt.Errorf("telegram: token is required")
	}
	return &Telegram{Token: token, Allow: s.Int64s("allow_chats")}, nil
}
