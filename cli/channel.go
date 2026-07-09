package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"
	"text/template"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/channel"
	// Imported for their init() so `channel list` sees the shipped drivers.
	_ "github.com/tamnd/tomo/pkg/channel/discord"
	_ "github.com/tamnd/tomo/pkg/channel/imessage"
	_ "github.com/tamnd/tomo/pkg/channel/slack"
	_ "github.com/tamnd/tomo/pkg/channel/telegram"
	_ "github.com/tamnd/tomo/pkg/channel/webchat"
)

func newChannelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channel",
		Short: "List channel drivers and scaffold new ones",
		Long: "A channel is a front door: a package that registers a driver by name,\n" +
			"which serve reaches through a registry. This command lists the drivers\n" +
			"compiled in and scaffolds a new one against the same interface, so a new\n" +
			"channel starts as reviewable Go in your tree, not runtime plugin code.",
	}
	cmd.AddCommand(newChannelListCmd(), newChannelScaffoldCmd())
	return cmd
}

func newChannelListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the channel drivers compiled into this binary",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			for _, name := range channel.Drivers() {
				fmt.Fprintf(w, "%s\n", name)
			}
			return w.Flush()
		},
	}
}

func newChannelScaffoldCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "scaffold <name>",
		Short: "Generate a starter channel adapter package",
		Long: "scaffold writes a new channel adapter: the interface methods stubbed,\n" +
			"the driver registered by name, and a config block documented. The output\n" +
			"is plain Go you review in a diff and finish by hand. Add its import to\n" +
			"cli/serve.go (the command prints the line) and it is wired in.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.ToLower(strings.TrimSpace(args[0]))
			if !validName.MatchString(name) {
				return fmt.Errorf("channel name %q: use lower-case letters, digits, and underscores, starting with a letter", name)
			}
			if slices.Contains(channel.Drivers(), name) {
				return fmt.Errorf("channel %q already has a driver; pick another name", name)
			}
			pkgDir := dir
			if pkgDir == "" {
				pkgDir = filepath.Join("pkg", "channel", name)
			}
			if _, err := os.Stat(pkgDir); err == nil {
				return fmt.Errorf("%s already exists; refusing to overwrite", pkgDir)
			}
			if err := os.MkdirAll(pkgDir, 0o755); err != nil {
				return err
			}
			file := filepath.Join(pkgDir, name+".go")
			body, err := renderChannel(name)
			if err != nil {
				return err
			}
			if err := os.WriteFile(file, body, 0o644); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "wrote %s\n\n", file)
			fmt.Fprintf(out, "next:\n")
			fmt.Fprintf(out, "  1. add this import to cli/serve.go, with the other channel drivers:\n")
			fmt.Fprintf(out, "       _ \"github.com/tamnd/tomo/pkg/channel/%s\"\n", name)
			fmt.Fprintf(out, "  2. fill in Run: read messages, build an Inbound, call the Handler\n")
			fmt.Fprintf(out, "  3. add a %s block under channels: in your config\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "target directory (default pkg/channel/<name>)")
	return cmd
}

// validName guards the scaffold: the name becomes a Go package name and a
// registry key, so it must be a bare identifier.
var validName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// renderChannel fills the adapter template for a channel name.
func renderChannel(name string) ([]byte, error) {
	t, err := template.New("channel").Parse(channelTemplate)
	if err != nil {
		return nil, err
	}
	data := struct {
		Name  string // package and registry name, e.g. "matrix"
		Type  string // exported type name, e.g. "Matrix"
		Title string // human title for comments, e.g. "Matrix"
	}{Name: name, Type: exportName(name), Title: exportName(name)}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

// exportName turns a channel name into an exported Go identifier: underscores
// become word breaks and each word is capitalized, so "google_chat" becomes
// "GoogleChat".
func exportName(name string) string {
	parts := strings.Split(name, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

// channelTemplate is the starter adapter. It compiles as written: Run returns a
// not-implemented error so the package builds and registers before the platform
// wiring is filled in.
const channelTemplate = `// Package {{.Name}} is a tomo channel adapter. It translates {{.Title}}
// messages into an Inbound the router understands, and renders the router's
// replies back. Everything above the adapter (sessions, the agent turn, the
// policy gate) is the router's job and is written once; keep this file thin.
package {{.Name}}

import (
	"context"
	"fmt"

	"github.com/tamnd/tomo/pkg/channel"
)

func init() { channel.Register("{{.Name}}", driver{}) }

// driver opens a {{.Title}} channel from its config block under channels.{{.Name}}.
type driver struct{}

// Open reads this channel's settings and builds the adapter. Return (nil, nil)
// to mean "configured but off" so an empty block is a no-op rather than an error.
func (driver) Open(s channel.Settings) (channel.Channel, error) {
	token := s.String("token")
	if token == "" {
		return nil, fmt.Errorf("{{.Name}}: token is required")
	}
	return &{{.Type}}{Token: token, Allow: s.Strings("allow")}, nil
}

// {{.Type}} serves an allowlisted set of chats.
type {{.Type}} struct {
	Token string
	Allow []string // ids permitted to talk to the agent
}

// Name is the channel's stable name, used in session keys and the audit log.
func (c *{{.Type}}) Name() string { return "{{.Name}}" }

// Caps declares what this channel can do so the router degrades gracefully:
// without Buttons an approval falls back to a yes/no text reply, without Stream
// the reply is sent once at the end.
func (c *{{.Type}}) Caps() channel.Caps {
	return channel.Caps{Media: false, Buttons: false, Stream: false}
}

// Run receives messages until ctx is cancelled. For each inbound message, build
// a channel.Inbound, hand it to h with a Reply and an Approver, and let the
// router take it from there. See pkg/channel/telegram for a worked example.
func (c *{{.Type}}) Run(ctx context.Context, h channel.Handler) error {
	return fmt.Errorf("{{.Name}}: Run is not implemented yet")
}
`
