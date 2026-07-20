package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/trace"
)

func newTracesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "traces",
		Short: "Inspect normalized model-call traces",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTraceSummary(cmd)
		},
	}
	cmd.AddCommand(newTraceListCmd())
	cmd.AddCommand(newTraceSummaryCmd())
	cmd.AddCommand(newTraceExportCmd())
	cmd.AddCommand(newTraceExportAllCmd())
	return cmd
}

func newTraceExportAllCmd() *cobra.Command {
	var filter trace.Filter
	cmd := &cobra.Command{
		Use:   "export-all OUTPUT_DIR",
		Short: "Materialize an upload-ready Hugging Face trace dataset",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			result, err := trace.ExportDataset(cfg.Tracing.Dir, args[0], filter)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "exported %d runs, %d bytes\n", result.Runs, result.Bytes)
			return nil
		},
	}
	addTraceFilterFlags(cmd, &filter)
	return cmd
}

func newTraceListCmd() *cobra.Command {
	var filter trace.Filter
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List runs by date, model, provider, or task",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			runs, err := trace.List(cfg.Tracing.Dir, filter)
			if err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd, runs)
			}
			out := cmd.OutOrStdout()
			if len(runs) == 0 {
				_, err := fmt.Fprintln(out, "no matching model traces")
				return err
			}
			fmt.Fprintln(out, "DATE        MODEL                         CALLS  TOKENS       COST USD  STATUS   TASK")
			for _, run := range runs {
				fmt.Fprintf(out, "%-10s  %-28s  %5d  %10d  %9.6f  %-7s  %s [%s]\n",
					run.Date, truncateCell(run.Model, 28), run.Calls, run.TotalTokens, run.CostUSD, run.Status,
					truncateCell(run.TaskLabel, 56), run.ID)
			}
			return nil
		},
	}
	addTraceFilterFlags(cmd, &filter)
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "write machine-readable JSON")
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum runs")
	return cmd
}

func newTraceSummaryCmd() *cobra.Command {
	var filter trace.Filter
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Aggregate trace volume, usage, failures, models, and tasks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTraceSummaryWith(cmd, filter, jsonOutput)
		},
	}
	addTraceFilterFlags(cmd, &filter)
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "write machine-readable JSON")
	return cmd
}

func newTraceExportCmd() *cobra.Command {
	var output string
	var format string
	cmd := &cobra.Command{
		Use:   "export RUN_ID",
		Short: "Export one run as Hugging Face STS JSONL or native JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			if output == "" {
				output = "-"
			}
			switch format {
			case "sts", "hf", "huggingface":
				return trace.ExportSTS(cfg.Tracing.Dir, args[0], output)
			case "native":
				return trace.ExportNative(cfg.Tracing.Dir, args[0], output)
			default:
				return fmt.Errorf("unknown trace format %q: want sts or native", format)
			}
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "output path (default stdout)")
	cmd.Flags().StringVar(&format, "format", "sts", "export format: sts or native")
	return cmd
}

func runTraceSummary(cmd *cobra.Command) error {
	return runTraceSummaryWith(cmd, trace.Filter{}, false)
}

func runTraceSummaryWith(cmd *cobra.Command, filter trace.Filter, jsonOutput bool) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	summary, err := trace.Summarize(cfg.Tracing.Dir, filter)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(cmd, summary)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "runs: %d, calls: %d, failed calls: %d\n", summary.Runs, summary.Calls, summary.FailedCalls)
	fmt.Fprintf(out, "models: %d, tasks: %d\n", summary.Models, summary.Tasks)
	fmt.Fprintf(out, "tokens: input %d (cache read %d, cache write %d), output %d (reasoning %d), total %d\n",
		summary.InputTokens, summary.CachedInputTokens, summary.CacheWriteInputTokens,
		summary.OutputTokens, summary.ReasoningTokens, summary.TotalTokens)
	fmt.Fprintf(out, "list cost: $%.9f USD across %d priced calls", summary.CostUSD, summary.PricedCalls)
	if summary.UnpricedCalls > 0 {
		fmt.Fprintf(out, ", %d unpriced calls", summary.UnpricedCalls)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "model time: %s\n", time.Duration(summary.DurationMS)*time.Millisecond)
	fmt.Fprintf(out, "deduplicated objects: %d, stored bytes: %d\n", summary.UniqueObjects, summary.ObjectBytes)
	return nil
}

func addTraceFilterFlags(cmd *cobra.Command, filter *trace.Filter) {
	cmd.Flags().StringVar(&filter.Model, "model", "", "exact model ID")
	cmd.Flags().StringVar(&filter.Provider, "provider", "", "exact provider name")
	cmd.Flags().StringVar(&filter.Task, "task", "", "task ID or label substring")
	cmd.Flags().StringVar(&filter.Date, "date", "", "run date in YYYY-MM-DD")
	var since string
	cmd.Flags().StringVar(&since, "since", "", "include runs at or after RFC3339 time")
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if filter.Date != "" {
			if _, err := time.Parse("2006-01-02", filter.Date); err != nil {
				return fmt.Errorf("invalid date %q: want YYYY-MM-DD", filter.Date)
			}
		}
		if since != "" {
			value, err := time.Parse(time.RFC3339, since)
			if err != nil {
				return fmt.Errorf("invalid since %q: want RFC3339", since)
			}
			filter.Since = value
		}
		return nil
	}
}

func writeJSON(cmd *cobra.Command, value any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func truncateCell(value string, width int) string {
	if len(value) <= width {
		return value
	}
	if width <= 3 {
		return value[:width]
	}
	return value[:width-3] + "..."
}
