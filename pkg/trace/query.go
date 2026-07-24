package trace

import (
	"path/filepath"
	"os"
	"sort"
	"strings"
	"time"
)

// Filter narrows trace queries. Date uses YYYY-MM-DD. Since is inclusive.
type Filter struct {
	Model    string
	Provider string
	Task     string
	Date     string
	Since    time.Time
	Limit    int
}

// Run is one provider instance with cumulative usage and priced cost.
type Run struct {
	ID                    string  `json:"id"`
	Date                  string  `json:"date"`
	StartedAt             string  `json:"started_at"`
	EndedAt               string  `json:"ended_at"`
	Provider              string  `json:"provider"`
	Model                 string  `json:"model"`
	TaskID                string  `json:"task_id"`
	TaskLabel             string  `json:"task_label"`
	Status                string  `json:"status"`
	Calls                 int     `json:"calls"`
	FailedCalls           int     `json:"failed_calls"`
	InputTokens           int     `json:"input_tokens"`
	CachedInputTokens     int     `json:"cached_input_tokens"`
	CacheWriteInputTokens int     `json:"cache_write_input_tokens"`
	OutputTokens          int     `json:"output_tokens"`
	ReasoningTokens       int     `json:"reasoning_tokens"`
	TotalTokens           int     `json:"total_tokens"`
	PricedCalls           int     `json:"priced_calls"`
	UnpricedCalls         int     `json:"unpriced_calls"`
	InputCostNanos        int64   `json:"input_cost_nanos"`
	CachedCostNanos       int64   `json:"cached_input_cost_nanos"`
	CacheWriteCostNanos   int64   `json:"cache_write_cost_nanos"`
	OutputCostNanos       int64   `json:"output_cost_nanos"`
	CostNanos             int64   `json:"cost_nanos"`
	CostUSD               float64 `json:"cost_usd"`
	DurationMS            int64   `json:"duration_ms"`
}

// Summary aggregates matching runs without loading prompt content.
type Summary struct {
	Runs                  int     `json:"runs"`
	Calls                 int     `json:"calls"`
	FailedCalls           int     `json:"failed_calls"`
	Models                int     `json:"models"`
	Tasks                 int     `json:"tasks"`
	InputTokens           int64   `json:"input_tokens"`
	CachedInputTokens     int64   `json:"cached_input_tokens"`
	CacheWriteInputTokens int64   `json:"cache_write_input_tokens"`
	OutputTokens          int64   `json:"output_tokens"`
	ReasoningTokens       int64   `json:"reasoning_tokens"`
	TotalTokens           int64   `json:"total_tokens"`
	PricedCalls           int64   `json:"priced_calls"`
	UnpricedCalls         int64   `json:"unpriced_calls"`
	InputCostNanos        int64   `json:"input_cost_nanos"`
	CachedCostNanos       int64   `json:"cached_input_cost_nanos"`
	CacheWriteCostNanos   int64   `json:"cache_write_cost_nanos"`
	OutputCostNanos       int64   `json:"output_cost_nanos"`
	CostNanos             int64   `json:"cost_nanos"`
	CostUSD               float64 `json:"cost_usd"`
	DurationMS            int64   `json:"duration_ms"`
	StoredRuns            int     `json:"stored_runs"`
	StoredBytes           int64   `json:"stored_bytes"`
}

// runFiles lists the rollout files in a trace directory.
func runFiles(dir string) ([]string, error) {
	return filepath.Glob(filepath.Join(dir, "*.jsonl"))
}

// scanAll reads every rollout in a directory into its cumulative row, dropping
// any file that does not decode as a rollout (a foreign .jsonl, or an empty one
// with no header). It is the single fold every query shares.
func scanAll(dir string, filter Filter) ([]Run, error) {
	paths, err := runFiles(dir)
	if err != nil {
		return nil, err
	}
	var out []Run
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		header, calls, err := scanRun(file)
		file.Close()
		if err != nil {
			continue
		}
		run := foldRun(header, calls)
		if run.ID == "" {
			continue
		}
		if !matchFilter(run, filter) {
			continue
		}
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out, nil
}

func matchFilter(run Run, filter Filter) bool {
	if filter.Model != "" && run.Model != filter.Model {
		return false
	}
	if filter.Provider != "" && run.Provider != filter.Provider {
		return false
	}
	if filter.Task != "" && run.TaskID != filter.Task && !strings.Contains(run.TaskLabel, filter.Task) {
		return false
	}
	if filter.Date != "" && run.Date != filter.Date {
		return false
	}
	if !filter.Since.IsZero() && run.StartedAt < stamp(filter.Since) {
		return false
	}
	return true
}

// List returns matching runs newest first.
func List(dir string, filter Filter) ([]Run, error) {
	runs, err := scanAll(dir, filter)
	if err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit == 0 {
		limit = 50
	}
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

// Summarize returns compact capability and efficiency totals for matching runs.
func Summarize(dir string, filter Filter) (Summary, error) {
	filter.Limit = -1
	runs, err := scanAll(dir, filter)
	if err != nil {
		return Summary{}, err
	}
	var out Summary
	models := map[string]struct{}{}
	tasks := map[string]struct{}{}
	for _, run := range runs {
		out.Runs++
		out.Calls += run.Calls
		out.FailedCalls += run.FailedCalls
		models[run.Model] = struct{}{}
		tasks[run.TaskID] = struct{}{}
		out.InputTokens += int64(run.InputTokens)
		out.CachedInputTokens += int64(run.CachedInputTokens)
		out.CacheWriteInputTokens += int64(run.CacheWriteInputTokens)
		out.OutputTokens += int64(run.OutputTokens)
		out.ReasoningTokens += int64(run.ReasoningTokens)
		out.TotalTokens += int64(run.TotalTokens)
		out.PricedCalls += int64(run.PricedCalls)
		out.UnpricedCalls += int64(run.UnpricedCalls)
		out.InputCostNanos += run.InputCostNanos
		out.CachedCostNanos += run.CachedCostNanos
		out.CacheWriteCostNanos += run.CacheWriteCostNanos
		out.OutputCostNanos += run.OutputCostNanos
		out.CostNanos += run.CostNanos
		out.DurationMS += run.DurationMS
	}
	out.Models = len(models)
	out.Tasks = len(tasks)
	out.CostUSD = nanosUSD(out.CostNanos)
	out.StoredRuns = out.Runs
	if paths, err := runFiles(dir); err == nil {
		for _, path := range paths {
			if info, err := os.Stat(path); err == nil {
				out.StoredBytes += info.Size()
			}
		}
	}
	return out, nil
}

// loadRun returns one run's cumulative row by id.
func loadRun(dir, id string) (Run, error) {
	header, calls, err := readRun(dir, id)
	if err != nil {
		return Run{}, err
	}
	return foldRun(header, calls), nil
}
