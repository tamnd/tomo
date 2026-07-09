package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/config"
	"github.com/tamnd/tomo/pkg/doctor"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the config, provider key, data dir, and channels",
		Long: "doctor runs tomo's startup preconditions and prints a line per check\n" +
			"with a named fix for anything wrong. serve runs the same checks on boot.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			path, err := cmd.Flags().GetString("config")
			if err != nil {
				return err
			}
			cfg, err := config.Load(path)
			if err != nil {
				// A parse or missing-file error already names its fix.
				fmt.Fprintf(out, "%s config: %v\n", mark(false), err)
				return errBadConfig
			}
			results := doctor.Check(cfg)
			for _, r := range results {
				fmt.Fprintf(out, "%s %s: %s\n", mark(r.OK), r.Name, r.Detail)
			}
			if !doctor.OK(results) {
				return errCheckFailed
			}
			fmt.Fprintln(out, "\nall good. next: tomo chat")
			return nil
		},
	}
}

var (
	errBadConfig   = errors.New("config did not load")
	errCheckFailed = errors.New("one or more checks failed")
)

// mark renders a pass/fail glyph. fang colors errors; the glyph keeps the line
// readable when output is piped or colorless.
func mark(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}
