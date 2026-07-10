package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/store"
)

func newSessionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sessions",
		Short: "List conversations in the ledger",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			st, err := store.Open(filepath.Join(cfg.DataDir, "tomo.db"))
			if err != nil {
				return err
			}
			defer st.Close()
			sessions, err := st.Sessions()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			th := themeFor(out)
			if len(sessions) == 0 {
				fmt.Fprintf(out, "%s\n", th.muted("no sessions yet · tomo chat --session <name>"))
				return nil
			}
			fmt.Fprintf(out, "%s\n\n", th.heading("SESSIONS"))
			// Build the rows first so the columns can be sized to their widest
			// visible cell before anything is painted.
			type row struct{ name, channel, msgs, updated string }
			rows := make([]row, len(sessions))
			wName, wChan, wMsgs := len("NAME"), len("CHANNEL"), len("MSGS")
			for i, s := range sessions {
				r := row{s.Name, s.Channel, itoa(s.Messages), s.Updated.Local().Format("2006-01-02 15:04")}
				rows[i] = r
				wName = max(wName, len(r.name))
				wChan = max(wChan, len(r.channel))
				wMsgs = max(wMsgs, len(r.msgs))
			}
			head := func(s string, w int) string { return padRight(th.paint(styleDim, s), w) }
			fmt.Fprintf(out, "  %s   %s   %s   %s\n",
				head("NAME", wName), head("CHANNEL", wChan), head("MSGS", wMsgs), th.paint(styleDim, "UPDATED"))
			for _, r := range rows {
				fmt.Fprintf(out, "  %s   %s   %s   %s\n",
					padRight(th.name(r.name), wName),
					padRight(th.muted(r.channel), wChan),
					padRight(th.muted(r.msgs), wMsgs),
					th.muted(r.updated))
			}
			return nil
		},
	}
}
