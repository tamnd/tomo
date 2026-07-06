package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tamnd/tomo/pkg/tool"
)

// maxAttachment caps a file the agent tries to send, so a stray path does not
// stream a huge blob through a chat.
const maxAttachment = 20 << 20

// attachTool lets the agent send a file it made (an image, chart, screenshot,
// or document) back through the channel it is talking on. It is bound to one
// exchange's reply, like the schedule tool is bound to one chat, so the file
// reaches the right conversation. On a channel that cannot carry files it tells
// the agent where the file is instead of failing.
func attachTool(reply Reply) tool.Tool {
	return tool.Tool{
		Name: "send_file",
		Description: "Send a file you have created (an image, chart, screenshot, or document) to the user through this " +
			"channel. Give the local path to the file; add a short caption if it helps.",
		Class: tool.ClassRead,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "local path to the file to send"},
				"caption": {"type": "string", "description": "optional note to show with the file"}
			},
			"required": ["path"]
		}`),
		Run: func(_ context.Context, input json.RawMessage) (string, error) {
			var v struct {
				Path    string `json:"path"`
				Caption string `json:"caption"`
			}
			if err := json.Unmarshal(input, &v); err != nil {
				return "", err
			}
			path := expandHome(strings.TrimSpace(v.Path))
			if path == "" {
				return "", fmt.Errorf("send_file: no path given")
			}
			info, err := os.Stat(path)
			if err != nil {
				return "", err
			}
			if info.IsDir() {
				return "", fmt.Errorf("send_file: %s is a directory", path)
			}
			if info.Size() > maxAttachment {
				return "", fmt.Errorf("send_file: %s is too large to send (%d bytes)", path, info.Size())
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}

			fr, ok := reply.(FileReply)
			if !ok {
				return "this channel cannot carry files, so nothing was sent. The file is ready at " + path, nil
			}
			name := filepath.Base(path)
			fr.File(Attachment{Name: name, Mime: mimeOf(name, data), Data: data, Caption: strings.TrimSpace(v.Caption)})
			return "sent " + name, nil
		},
	}
}

// mimeOf picks a media type from the filename extension, falling back to
// sniffing the bytes when the extension is unknown.
func mimeOf(name string, data []byte) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".md":
		return "text/plain"
	default:
		return http.DetectContentType(data)
	}
}

// expandHome resolves a leading ~ to the user's home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
