package trace

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/tamnd/tomo/pkg/provider"
)

// ImportCall is one historical model call reconstructed by a migration tool.
type ImportCall struct {
	StartedAt time.Time
	Duration  time.Duration
	Request   provider.Request
	Response  *provider.Response
	Error     string
}

// ImportRun is one historical session. ID should be deterministic so a
// migration can resume safely without creating duplicate runs.
type ImportRun struct {
	ID        string
	StartedAt time.Time
	Provider  string
	Pricing   *Pricing
	Calls     []ImportCall
}

// ImportResult reports whether a historical run was added or already complete.
type ImportResult struct {
	RunID           string
	ImportedCalls   int
	ExistingCalls   int
	AlreadyComplete bool
}

// ImportHistoricalRun inserts a reconstructed session into the normalized
// ledger. Existing call sequences are skipped, which makes the operation safe
// to resume after interruption.
func ImportHistoricalRun(ctx context.Context, dir string, run ImportRun) (ImportResult, error) {
	if run.ID == "" {
		return ImportResult{}, fmt.Errorf("trace import: run ID is empty")
	}
	if run.StartedAt.IsZero() {
		return ImportResult{}, fmt.Errorf("trace import %s: start time is empty", run.ID)
	}
	db, err := open(dir)
	if err != nil {
		return ImportResult{}, err
	}
	defer db.Close()
	result := ImportResult{RunID: run.ID}
	var existing int
	err = db.QueryRowContext(ctx, `SELECT calls FROM runs WHERE id = ?`, run.ID).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return result, err
	}
	result.ExistingCalls = existing
	if existing == len(run.Calls) && existing != 0 {
		result.AlreadyComplete = true
		return result, nil
	}
	tracer := &tracedProvider{
		opts:  Options{Dir: dir, Provider: run.Provider, Pricing: run.Pricing},
		runID: run.ID, started: run.StartedAt.UTC(), db: db,
	}
	for index, call := range run.Calls {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		sequence := int64(index + 1)
		var present int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM calls WHERE run_id = ? AND sequence = ?`, run.ID, sequence).Scan(&present); err != nil {
			return result, err
		}
		if present != 0 {
			continue
		}
		started := call.StartedAt.UTC()
		ended := started.Add(call.Duration)
		var callErr error
		if call.Error != "" {
			callErr = fmt.Errorf("%s", call.Error)
		}
		if err := tracer.record(call.Request, call.Response, callErr, sequence, started, ended); err != nil {
			return result, fmt.Errorf("trace import %s call %d: %w", run.ID, sequence, err)
		}
		result.ImportedCalls++
	}
	return result, nil
}
