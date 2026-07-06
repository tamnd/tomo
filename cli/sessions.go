package cli

import (
	"fmt"
	"path/filepath"
	"text/tabwriter"

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
			if len(sessions) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no sessions yet (tomo chat --session <name>)")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tCHANNEL\tMESSAGES\tUPDATED")
			for _, s := range sessions {
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", s.Name, s.Channel, s.Messages, s.Updated.Local().Format("2006-01-02 15:04"))
			}
			return w.Flush()
		},
	}
}
