package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/schedule"
	"github.com/tamnd/tomo/pkg/store"
)

func newCronCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cron",
		Short: "Schedule prompts to run on their own",
		Long: "cron manages the jobs tomo runs unattended. A job is a prompt on a\n" +
			"schedule, aimed at a channel and chat; when serve is running it fires\n" +
			"the prompt and posts the result there.",
	}
	cmd.AddCommand(newCronAddCmd(), newCronListCmd(), newCronRmCmd(), newCronLogCmd())
	return cmd
}

// openStore opens the ledger for the cron subcommands.
func openStore(cmd *cobra.Command) (*store.Store, error) {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return nil, err
	}
	return store.Open(filepath.Join(cfg.DataDir, "tomo.db"))
}

func newCronAddCmd() *cobra.Command {
	var channelName, chat string
	cmd := &cobra.Command{
		Use:   "add <schedule> <prompt>",
		Short: "Add a scheduled prompt",
		Long: "Add a job. The schedule is five-field cron (in local time), or a\n" +
			"macro like @daily, or @every 30m. Example:\n" +
			"  tomo cron add '0 8 * * *' 'summarize my unread mail' --channel telegram --chat 123",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := schedule.Parse(args[0]); err != nil {
				return fmt.Errorf("bad schedule: %w", err)
			}
			if channelName == "" || chat == "" {
				return fmt.Errorf("both --channel and --chat are required")
			}
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer st.Close()
			id, err := st.AddJob(args[0], args[1], channelName, chat)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added job %d\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&channelName, "channel", "", "channel to post results to (telegram, discord, slack, imessage)")
	cmd.Flags().StringVar(&chat, "chat", "", "chat id within that channel")
	return cmd
}

func newCronListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List scheduled jobs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer st.Close()
			jobs, err := st.Jobs()
			if err != nil {
				return err
			}
			if len(jobs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no jobs")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSCHEDULE\tCHANNEL\tCHAT\tON\tLAST RUN\tPROMPT")
			for _, j := range jobs {
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
					j.ID, j.Spec, j.Channel, j.Chat, onOff(j.Enabled), lastRun(j.LastRun), truncate(j.Prompt, 40))
			}
			return w.Flush()
		},
	}
}

func newCronRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id>",
		Short: "Remove a scheduled job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("job id must be a number")
			}
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer st.Close()
			ok, err := st.RemoveJob(id)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no job %d", id)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed job %d\n", id)
			return nil
		},
	}
}

func newCronLogCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show recent job runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStore(cmd)
			if err != nil {
				return err
			}
			defer st.Close()
			runs, err := st.Runs(limit)
			if err != nil {
				return err
			}
			if len(runs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no runs yet")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "JOB\tWHEN\tOK\tOUTPUT")
			for _, r := range runs {
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
					r.JobID, r.Started.Local().Format("2006-01-02 15:04"), yesNo(r.OK), truncate(oneLine(r.Output), 60))
			}
			return w.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "how many runs to show")
	return cmd
}

func onOff(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func yesNo(b bool) string {
	if b {
		return "ok"
	}
	return "err"
}

func lastRun(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func oneLine(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		out = append(out, r)
	}
	return string(out)
}
