package trace

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/tamnd/tomo/pkg/provider"
)

type callRecord struct {
	Sequence              int64              `json:"sequence"`
	StartedAt             string             `json:"started_at"`
	DurationMS            int64              `json:"duration_ms"`
	System                string             `json:"system"`
	Tools                 []stableTool       `json:"tools"`
	Messages              []provider.Message `json:"messages"`
	Response              *recordedResponse  `json:"response,omitempty"`
	Error                 json.RawMessage    `json:"error,omitempty"`
	StopReason            string             `json:"stop_reason"`
	InputTokens           int                `json:"input_tokens"`
	CachedInputTokens     int                `json:"cached_input_tokens"`
	CacheWriteInputTokens int                `json:"cache_write_input_tokens"`
	OutputTokens          int                `json:"output_tokens"`
	ReasoningTokens       int                `json:"reasoning_tokens"`
	TotalTokens           int                `json:"total_tokens"`
	PricingKnown          bool               `json:"pricing_known"`
	Pricing               Pricing            `json:"pricing_usd_per_million"`
	InputCostNanos        int64              `json:"input_cost_nanos"`
	CachedCostNanos       int64              `json:"cached_input_cost_nanos"`
	CacheWriteCostNanos   int64              `json:"cache_write_cost_nanos"`
	OutputCostNanos       int64              `json:"output_cost_nanos"`
	CostNanos             int64              `json:"cost_nanos"`
	CostUSD               float64            `json:"cost_usd"`
}

// loadCalls reads a run's calls from its rollout, in the order they were made.
func loadCalls(dir, runID string) ([]callRecord, error) {
	_, calls, err := readRun(dir, runID)
	return calls, err
}

func writeExport(output string, payload []byte) error {
	if output == "" || output == "-" {
		_, err := os.Stdout.Write(payload)
		return err
	}
	dir := filepath.Dir(output)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".trace-export-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, output)
}
