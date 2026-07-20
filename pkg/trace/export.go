package trace

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

type callRow struct {
	record                                       callRecord
	id                                           int64
	systemHash, toolsHash, responseHash, errHash string
}

func loadCalls(db *sql.DB, runID string) ([]callRecord, error) {
	rows, err := db.Query(`SELECT id, sequence, started_at, duration_ms, system_hash,
		tools_hash, response_hash, error_hash, stop_reason, input_tokens,
		cached_input_tokens, cache_write_input_tokens, output_tokens, reasoning_tokens,
		total_tokens, pricing_known, input_price, cached_input_price, cache_write_price,
		output_price, input_cost_nanos, cached_cost_nanos, cache_write_cost_nanos,
		output_cost_nanos, cost_nanos FROM calls WHERE run_id = ? ORDER BY sequence`, runID)
	if err != nil {
		return nil, err
	}
	var raw []callRow
	for rows.Next() {
		var item callRow
		if err := rows.Scan(&item.id, &item.record.Sequence, &item.record.StartedAt,
			&item.record.DurationMS, &item.systemHash, &item.toolsHash,
			&item.responseHash, &item.errHash, &item.record.StopReason,
			&item.record.InputTokens, &item.record.CachedInputTokens,
			&item.record.CacheWriteInputTokens, &item.record.OutputTokens,
			&item.record.ReasoningTokens, &item.record.TotalTokens,
			&item.record.PricingKnown, &item.record.Pricing.InputUSDPerMillion,
			&item.record.Pricing.CachedUSDPerMillion,
			&item.record.Pricing.CacheWriteUSDPerMillion,
			&item.record.Pricing.OutputUSDPerMillion, &item.record.InputCostNanos,
			&item.record.CachedCostNanos, &item.record.CacheWriteCostNanos,
			&item.record.OutputCostNanos, &item.record.CostNanos); err != nil {
			rows.Close()
			return nil, err
		}
		raw = append(raw, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]callRecord, 0, len(raw))
	for _, item := range raw {
		if err := decodeObject(db, item.systemHash, &item.record.System); err != nil {
			return nil, err
		}
		if err := decodeObject(db, item.toolsHash, &item.record.Tools); err != nil {
			return nil, err
		}
		if item.responseHash != "" {
			var response recordedResponse
			if err := decodeObject(db, item.responseHash, &response); err != nil {
				return nil, err
			}
			item.record.Response = &response
		}
		if item.errHash != "" {
			payload, err := object(db, item.errHash)
			if err != nil {
				return nil, err
			}
			item.record.Error = payload
		}
		messageRows, err := db.Query(`SELECT object_hash FROM call_messages WHERE call_id = ? ORDER BY position`, item.id)
		if err != nil {
			return nil, err
		}
		var hashes []string
		for messageRows.Next() {
			var hash string
			if err := messageRows.Scan(&hash); err != nil {
				messageRows.Close()
				return nil, err
			}
			hashes = append(hashes, hash)
		}
		if err := messageRows.Close(); err != nil {
			return nil, err
		}
		for _, hash := range hashes {
			var message provider.Message
			if err := decodeObject(db, hash, &message); err != nil {
				return nil, err
			}
			item.record.Messages = append(item.record.Messages, message)
		}
		item.record.CostUSD = nanosUSD(item.record.CostNanos)
		out = append(out, item.record)
	}
	return out, nil
}

func decodeObject(db *sql.DB, hash string, target any) error {
	payload, err := object(db, hash)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("trace object %s: %w", hash, err)
	}
	return nil
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
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, output)
}
