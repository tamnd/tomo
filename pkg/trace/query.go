package trace

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
	UniqueObjects         int     `json:"unique_objects"`
	ObjectBytes           int64   `json:"object_bytes"`
}

const runColumns = `id, run_date, started_at, ended_at, provider, model,
	task_id, task_label, status, calls, failed_calls, input_tokens,
	cached_input_tokens, cache_write_input_tokens, output_tokens, reasoning_tokens,
	total_tokens, priced_calls, unpriced_calls, input_cost_nanos, cached_cost_nanos,
	cache_write_cost_nanos, output_cost_nanos, cost_nanos, duration_ms`

func scanRun(scanner interface{ Scan(...any) error }) (Run, error) {
	var run Run
	err := scanner.Scan(&run.ID, &run.Date, &run.StartedAt, &run.EndedAt,
		&run.Provider, &run.Model, &run.TaskID, &run.TaskLabel, &run.Status,
		&run.Calls, &run.FailedCalls, &run.InputTokens, &run.CachedInputTokens,
		&run.CacheWriteInputTokens, &run.OutputTokens, &run.ReasoningTokens,
		&run.TotalTokens, &run.PricedCalls, &run.UnpricedCalls, &run.InputCostNanos,
		&run.CachedCostNanos, &run.CacheWriteCostNanos, &run.OutputCostNanos,
		&run.CostNanos, &run.DurationMS)
	run.CostUSD = nanosUSD(run.CostNanos)
	return run, err
}

// List returns matching runs newest first.
func List(dir string, filter Filter) ([]Run, error) {
	db, err := open(dir)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	where, args := filterSQL(filter)
	limit := filter.Limit
	if limit == 0 {
		limit = 50
	}
	query := `SELECT ` + runColumns + ` FROM runs ` + where + ` ORDER BY started_at DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// Summarize returns compact capability and efficiency totals for matching runs.
func Summarize(dir string, filter Filter) (Summary, error) {
	db, err := open(dir)
	if err != nil {
		return Summary{}, err
	}
	defer db.Close()
	where, args := filterSQL(filter)
	var out Summary
	err = db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(calls),0), COALESCE(SUM(failed_calls),0),
		COUNT(DISTINCT model), COUNT(DISTINCT task_id), COALESCE(SUM(input_tokens),0),
		COALESCE(SUM(cached_input_tokens),0), COALESCE(SUM(cache_write_input_tokens),0),
		COALESCE(SUM(output_tokens),0), COALESCE(SUM(reasoning_tokens),0), COALESCE(SUM(total_tokens),0),
		COALESCE(SUM(priced_calls),0), COALESCE(SUM(unpriced_calls),0),
		COALESCE(SUM(input_cost_nanos),0), COALESCE(SUM(cached_cost_nanos),0),
		COALESCE(SUM(cache_write_cost_nanos),0), COALESCE(SUM(output_cost_nanos),0),
		COALESCE(SUM(cost_nanos),0), COALESCE(SUM(duration_ms),0) FROM runs `+where, args...).Scan(
		&out.Runs, &out.Calls, &out.FailedCalls, &out.Models, &out.Tasks,
		&out.InputTokens, &out.CachedInputTokens, &out.CacheWriteInputTokens,
		&out.OutputTokens, &out.ReasoningTokens, &out.TotalTokens, &out.PricedCalls,
		&out.UnpricedCalls, &out.InputCostNanos, &out.CachedCostNanos,
		&out.CacheWriteCostNanos, &out.OutputCostNanos, &out.CostNanos, &out.DurationMS)
	if err != nil {
		return Summary{}, err
	}
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size_bytes),0) FROM objects`).Scan(&out.UniqueObjects, &out.ObjectBytes); err != nil {
		return Summary{}, err
	}
	out.CostUSD = nanosUSD(out.CostNanos)
	return out, nil
}

func filterSQL(filter Filter) (string, []any) {
	where := "WHERE 1=1"
	var args []any
	add := func(clause string, value any) {
		where += " AND " + clause
		args = append(args, value)
	}
	if filter.Model != "" {
		add("model = ?", filter.Model)
	}
	if filter.Provider != "" {
		add("provider = ?", filter.Provider)
	}
	if filter.Task != "" {
		add("(task_id = ? OR task_label LIKE '%' || ? || '%')", filter.Task)
		args = append(args, filter.Task)
	}
	if filter.Date != "" {
		add("run_date = ?", filter.Date)
	}
	if !filter.Since.IsZero() {
		add("started_at >= ?", stamp(filter.Since))
	}
	return where, args
}

func loadRun(db *sql.DB, id string) (Run, error) {
	run, err := scanRun(db.QueryRow(`SELECT `+runColumns+` FROM runs WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Run{}, fmt.Errorf("trace run %q not found", id)
	}
	return run, err
}

func object(db *sql.DB, hash string) (json.RawMessage, error) {
	var payload []byte
	if err := db.QueryRow(`SELECT payload FROM objects WHERE hash = ?`, hash).Scan(&payload); err != nil {
		return nil, fmt.Errorf("trace object %s: %w", hash, err)
	}
	return json.RawMessage(payload), nil
}

func nanosUSD(value int64) float64 { return float64(value) / 1_000_000_000 }
