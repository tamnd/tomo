package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/policy"
)

func newWatchCmd() *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Live, read-only view of tool calls and gate decisions",
		Long: "watch renders the audit log as a readable stream: each tool call, its\n" +
			"class, the gate's decision, whether it ran, and the session's taint\n" +
			"state. It is read-only; no control action travels over it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			path := filepath.Join(cfg.DataDir, "audit.log")
			return watchLog(cmd, path, follow)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", true, "keep watching for new entries (--follow=false to dump and exit)")
	return cmd
}

// watchLog renders the existing audit log, then, when follow is set, keeps
// rendering appended lines until the context is cancelled. It waits for the
// file to appear so `tomo watch` can be started before the first turn writes it.
func watchLog(cmd *cobra.Command, path string, follow bool) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	var f *os.File
	for {
		var err error
		f, err = os.Open(path)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) || !follow {
			return fmt.Errorf("no audit log at %s yet (run a turn, or start serve)", path)
		}
		if !sleep(ctx, 500*time.Millisecond) {
			return nil
		}
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 && strings.HasSuffix(line, "\n") {
			fmt.Fprintln(out, renderLine(strings.TrimRight(line, "\n")))
			continue
		}
		// A short read means we hit the end; keep any partial line for next time.
		if err == io.EOF {
			if !follow {
				if strings.TrimSpace(line) != "" {
					fmt.Fprintln(out, renderLine(strings.TrimSpace(line)))
				}
				return nil
			}
			if len(line) > 0 {
				// Unread the partial line by seeking back so the next read sees it whole.
				if off, serr := f.Seek(-int64(len(line)), io.SeekCurrent); serr == nil {
					_ = off
					reader.Reset(f)
				}
			}
			if !sleep(ctx, 300*time.Millisecond) {
				return nil
			}
			continue
		}
		if err != nil {
			return err
		}
	}
}

// renderLine turns one audit log line into a readable row. A line that does not
// parse is passed through raw rather than dropped, so nothing is hidden.
func renderLine(line string) string {
	var e policy.Entry
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		return line
	}
	return renderEntry(e)
}

// renderEntry formats one audit entry: time, decision, tool and class, whether
// it ran, the taint flag, and the reason. Kept pure for testing.
func renderEntry(e policy.Entry) string {
	t := e.Time
	if parsed, err := time.Parse(time.RFC3339, e.Time); err == nil {
		t = parsed.Format("15:04:05")
	}

	outcome := "ran"
	if !e.Allowed {
		outcome = "blocked"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s  %-5s %-8s %-16s %s", t, e.Decision, e.Class, e.Tool, outcome)
	if e.Tainted {
		b.WriteString(" · tainted")
	}
	if e.Reason != "" {
		fmt.Fprintf(&b, " · %s", e.Reason)
	}
	return b.String()
}

// sleep waits for d or until the context is cancelled, returning false when it
// was cancelled so the caller can stop.
func sleep(ctx interface{ Done() <-chan struct{} }, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
