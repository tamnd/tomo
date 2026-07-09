package imessage

import (
	"github.com/tamnd/tomo/pkg/channel"
)

func init() { channel.Register("imessage", driver{}) }

// driver opens the iMessage channel from its config block. It registers on
// every platform so the config validates everywhere; the channel itself only
// does anything on macOS, where the non-darwin build's Run reports that.
type driver struct{}

// Open reads the allowed handles and an optional chat.db path. The presence of
// an imessage block is the enable switch, so there is no separate flag.
func (driver) Open(s channel.Settings) (channel.Channel, error) {
	return &IMessage{Allow: s.Strings("allow_handles"), DBPath: s.String("db_path")}, nil
}
