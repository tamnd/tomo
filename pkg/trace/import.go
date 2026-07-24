package trace

import (
	"context"
	"fmt"
	"os"
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

// ImportHistoricalRun appends a reconstructed session to the rollout store.
// Call sequences already present in the run's file are skipped, which makes the
// operation safe to resume after interruption.
func ImportHistoricalRun(ctx context.Context, dir string, run ImportRun) (ImportResult, error) {
	if run.ID == "" {
		return ImportResult{}, fmt.Errorf("trace import: run ID is empty")
	}
	if run.StartedAt.IsZero() {
		return ImportResult{}, fmt.Errorf("trace import %s: start time is empty", run.ID)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ImportResult{}, fmt.Errorf("trace import: create directory: %w", err)
	}
	present := map[int64]struct{}{}
	if _, calls, err := readRun(dir, run.ID); err == nil {
		for _, call := range calls {
			present[call.Sequence] = struct{}{}
		}
	}
	result := ImportResult{RunID: run.ID, ExistingCalls: len(present)}
	if len(present) == len(run.Calls) && len(present) != 0 {
		result.AlreadyComplete = true
		return result, nil
	}
	file, err := os.OpenFile(runPath(dir, run.ID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return result, fmt.Errorf("trace import: open run log: %w", err)
	}
	defer file.Close()
	tracer := &tracedProvider{
		opts:  Options{Dir: dir, Provider: run.Provider, Pricing: run.Pricing},
		runID: run.ID, started: run.StartedAt.UTC(), file: file, wroteHeader: len(present) > 0,
	}
	for index, call := range run.Calls {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		sequence := int64(index + 1)
		if _, ok := present[sequence]; ok {
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
