// Package trace records model calls in a normalized, content-addressed ledger.
// Repeated system prompts, tool schemas, messages, and responses are stored once
// and referenced by hash from each call. SQLite indexes runs by date, model,
// provider, and task so later analysis does not need to scan raw wire logs.
package trace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/tamnd/tomo/pkg/provider"
)

// Options identifies the trace store and upstream provider.
type Options struct {
	Dir            string
	Provider       string
	Pricing        *Pricing
	PricingByModel map[string]Pricing
	OnError        func(error)
}

// Pricing is a list-price snapshot in USD per one million tokens. Supplying a
// zero-valued Pricing marks a free or locally hosted model as priced at zero.
type Pricing struct {
	InputUSDPerMillion      float64 `json:"input_usd_per_million"`
	CachedUSDPerMillion     float64 `json:"cached_input_usd_per_million"`
	CacheWriteUSDPerMillion float64 `json:"cache_write_usd_per_million"`
	OutputUSDPerMillion     float64 `json:"output_usd_per_million"`
}

type tracedProvider struct {
	base     provider.Provider
	opts     Options
	runID    string
	started  time.Time
	sequence atomic.Int64
	db       *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS objects (
  hash       TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  payload    BLOB NOT NULL,
  size_bytes INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runs (
  id                  TEXT PRIMARY KEY,
  run_date            TEXT NOT NULL,
  started_at          TEXT NOT NULL,
  ended_at            TEXT NOT NULL DEFAULT '',
  provider            TEXT NOT NULL,
  model               TEXT NOT NULL DEFAULT '',
  task_id             TEXT NOT NULL DEFAULT '',
  task_label          TEXT NOT NULL DEFAULT '',
  status              TEXT NOT NULL DEFAULT 'running',
  calls               INTEGER NOT NULL DEFAULT 0,
  failed_calls        INTEGER NOT NULL DEFAULT 0,
  input_tokens        INTEGER NOT NULL DEFAULT 0,
  cached_input_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens       INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens    INTEGER NOT NULL DEFAULT 0,
  total_tokens        INTEGER NOT NULL DEFAULT 0,
  priced_calls        INTEGER NOT NULL DEFAULT 0,
  unpriced_calls      INTEGER NOT NULL DEFAULT 0,
  input_cost_nanos    INTEGER NOT NULL DEFAULT 0,
  cached_cost_nanos   INTEGER NOT NULL DEFAULT 0,
  cache_write_cost_nanos INTEGER NOT NULL DEFAULT 0,
  output_cost_nanos   INTEGER NOT NULL DEFAULT 0,
  cost_nanos          INTEGER NOT NULL DEFAULT 0,
  duration_ms         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS runs_date_model ON runs(run_date, model);
CREATE INDEX IF NOT EXISTS runs_task ON runs(task_id, started_at);
CREATE INDEX IF NOT EXISTS runs_provider ON runs(provider, started_at);
CREATE TABLE IF NOT EXISTS calls (
  id                  INTEGER PRIMARY KEY,
  run_id              TEXT NOT NULL REFERENCES runs(id),
  sequence            INTEGER NOT NULL,
  started_at          TEXT NOT NULL,
  duration_ms         INTEGER NOT NULL,
  system_hash         TEXT NOT NULL DEFAULT '',
  tools_hash          TEXT NOT NULL DEFAULT '',
  response_hash       TEXT NOT NULL DEFAULT '',
  error_hash          TEXT NOT NULL DEFAULT '',
  stop_reason         TEXT NOT NULL DEFAULT '',
  input_tokens        INTEGER NOT NULL DEFAULT 0,
  cached_input_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens       INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens    INTEGER NOT NULL DEFAULT 0,
  total_tokens        INTEGER NOT NULL DEFAULT 0,
  pricing_known       INTEGER NOT NULL DEFAULT 0,
  input_price         REAL NOT NULL DEFAULT 0,
  cached_input_price  REAL NOT NULL DEFAULT 0,
  cache_write_price   REAL NOT NULL DEFAULT 0,
  output_price        REAL NOT NULL DEFAULT 0,
  input_cost_nanos    INTEGER NOT NULL DEFAULT 0,
  cached_cost_nanos   INTEGER NOT NULL DEFAULT 0,
  cache_write_cost_nanos INTEGER NOT NULL DEFAULT 0,
  output_cost_nanos   INTEGER NOT NULL DEFAULT 0,
  cost_nanos          INTEGER NOT NULL DEFAULT 0,
  UNIQUE(run_id, sequence)
);
CREATE INDEX IF NOT EXISTS calls_run ON calls(run_id, sequence);
CREATE TABLE IF NOT EXISTS call_messages (
  call_id      INTEGER NOT NULL REFERENCES calls(id),
  position     INTEGER NOT NULL,
  object_hash  TEXT NOT NULL REFERENCES objects(hash),
  PRIMARY KEY(call_id, position)
);
`

// Wrap validates the trace ledger and returns a transparent provider wrapper.
func Wrap(base provider.Provider, opts Options) (provider.Provider, error) {
	if base == nil {
		return nil, fmt.Errorf("trace: provider is nil")
	}
	if strings.TrimSpace(opts.Dir) == "" {
		return nil, fmt.Errorf("trace: directory is empty")
	}
	db, err := open(opts.Dir)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &tracedProvider{base: base, opts: opts, runID: newRunID(now), started: now, db: db}, nil
}

func (t *tracedProvider) Stream(ctx context.Context, req provider.Request, emit func(provider.Event)) (*provider.Response, error) {
	sequence := t.sequence.Add(1)
	started := time.Now().UTC()
	resp, callErr := t.base.Stream(ctx, req, emit)
	ended := time.Now().UTC()
	if err := t.record(req, resp, callErr, sequence, started, ended); err != nil && t.opts.OnError != nil {
		t.opts.OnError(err)
	}
	return resp, callErr
}

func (t *tracedProvider) record(req provider.Request, resp *provider.Response, callErr error, sequence int64, started, ended time.Time) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = t.recordOnce(req, resp, callErr, sequence, started, ended)
		if err == nil || !databaseBusy(err) {
			return err
		}
		time.Sleep(time.Duration(10*(1<<attempt)) * time.Millisecond)
	}
	return err
}

func (t *tracedProvider) recordOnce(req provider.Request, resp *provider.Response, callErr error, sequence int64, started, ended time.Time) error {
	tx, err := t.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	taskID, taskLabel := taskOf(req)
	_, err = tx.Exec(`INSERT INTO runs(id, run_date, started_at, provider, model, task_id, task_label)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  model = CASE WHEN runs.model = '' THEN excluded.model ELSE runs.model END,
		  task_id = CASE WHEN runs.task_id = '' THEN excluded.task_id ELSE runs.task_id END,
		  task_label = CASE WHEN runs.task_label = '' THEN excluded.task_label ELSE runs.task_label END`,
		t.runID, t.started.Format("2006-01-02"), stamp(t.started), t.opts.Provider, req.Model, taskID, taskLabel)
	if err != nil {
		return err
	}

	systemHash, err := putObject(tx, "system", req.System)
	if err != nil {
		return err
	}
	toolsHash, err := putObject(tx, "tools", stableTools(req.Tools))
	if err != nil {
		return err
	}
	responseHash := ""
	stopReason := ""
	usage := provider.Usage{}
	if resp != nil {
		responseHash, err = putObject(tx, "response", stableResponse(resp))
		if err != nil {
			return err
		}
		stopReason = resp.StopReason
		usage = resp.Usage.Normalize()
	}
	errorHash := ""
	failed := 0
	if callErr != nil {
		errorHash, err = putObject(tx, "error", map[string]string{"message": callErr.Error()})
		if err != nil {
			return err
		}
		failed = 1
	}
	duration := ended.Sub(started).Milliseconds()
	pricing := t.opts.Pricing
	if modelPricing, ok := t.opts.PricingByModel[req.Model]; ok {
		pricing = &modelPricing
	}
	cost := priceUsage(usage, pricing)
	res, err := tx.Exec(`INSERT INTO calls(
		run_id, sequence, started_at, duration_ms, system_hash, tools_hash,
		response_hash, error_hash, stop_reason, input_tokens, cached_input_tokens,
		cache_write_input_tokens, output_tokens, reasoning_tokens, total_tokens,
		pricing_known, input_price, cached_input_price, cache_write_price, output_price,
		input_cost_nanos, cached_cost_nanos, cache_write_cost_nanos, output_cost_nanos, cost_nanos)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.runID, sequence, stamp(started), duration, systemHash, toolsHash,
		responseHash, errorHash, stopReason, usage.InputTokens, usage.CachedInputTokens,
		usage.CacheWriteInputTokens, usage.OutputTokens, usage.ReasoningTokens, usage.TotalTokens,
		cost.Known, cost.InputPrice, cost.CachedPrice, cost.CacheWritePrice, cost.OutputPrice,
		cost.InputNanos, cost.CachedNanos, cost.CacheWriteNanos, cost.OutputNanos, cost.TotalNanos)
	if err != nil {
		return err
	}
	callID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for i, message := range req.Messages {
		hash, err := putObject(tx, "message", message)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO call_messages(call_id, position, object_hash) VALUES(?, ?, ?)`, callID, i, hash); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`UPDATE runs SET
		ended_at = ?, calls = calls + 1, failed_calls = failed_calls + ?,
		input_tokens = input_tokens + ?, cached_input_tokens = cached_input_tokens + ?,
		cache_write_input_tokens = cache_write_input_tokens + ?, output_tokens = output_tokens + ?,
		reasoning_tokens = reasoning_tokens + ?, total_tokens = total_tokens + ?,
		priced_calls = priced_calls + ?, unpriced_calls = unpriced_calls + ?,
		input_cost_nanos = input_cost_nanos + ?, cached_cost_nanos = cached_cost_nanos + ?,
		cache_write_cost_nanos = cache_write_cost_nanos + ?, output_cost_nanos = output_cost_nanos + ?,
		cost_nanos = cost_nanos + ?, duration_ms = duration_ms + ?,
		status = CASE WHEN failed_calls + ? > 0 THEN 'partial' ELSE 'complete' END
		WHERE id = ?`, stamp(ended), failed, usage.InputTokens, usage.CachedInputTokens,
		usage.CacheWriteInputTokens, usage.OutputTokens, usage.ReasoningTokens, usage.TotalTokens,
		boolInt(cost.Known), boolInt(!cost.Known), cost.InputNanos, cost.CachedNanos,
		cost.CacheWriteNanos, cost.OutputNanos, cost.TotalNanos, duration, failed, t.runID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func databaseBusy(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "sqlite_busy") || strings.Contains(message, "sqlite_locked")
}

type pricedUsage struct {
	Known           bool
	InputPrice      float64
	CachedPrice     float64
	CacheWritePrice float64
	OutputPrice     float64
	InputNanos      int64
	CachedNanos     int64
	CacheWriteNanos int64
	OutputNanos     int64
	TotalNanos      int64
}

func priceUsage(usage provider.Usage, pricing *Pricing) pricedUsage {
	if pricing == nil {
		return pricedUsage{}
	}
	fresh := usage.InputTokens - usage.CachedInputTokens - usage.CacheWriteInputTokens
	if fresh < 0 {
		fresh = 0
	}
	out := pricedUsage{
		Known: true, InputPrice: pricing.InputUSDPerMillion,
		CachedPrice:     pricing.CachedUSDPerMillion,
		CacheWritePrice: pricing.CacheWriteUSDPerMillion,
		OutputPrice:     pricing.OutputUSDPerMillion,
	}
	out.InputNanos = tokenCostNanos(fresh, out.InputPrice)
	out.CachedNanos = tokenCostNanos(usage.CachedInputTokens, out.CachedPrice)
	out.CacheWriteNanos = tokenCostNanos(usage.CacheWriteInputTokens, out.CacheWritePrice)
	out.OutputNanos = tokenCostNanos(usage.OutputTokens, out.OutputPrice)
	out.TotalNanos = out.InputNanos + out.CachedNanos + out.CacheWriteNanos + out.OutputNanos
	return out
}

func tokenCostNanos(tokens int, usdPerMillion float64) int64 {
	return int64(math.Round(float64(tokens) * usdPerMillion * 1000))
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

type stableTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

func stableTools(tools []provider.Tool) []stableTool {
	out := make([]stableTool, len(tools))
	for i, tool := range tools {
		out[i] = stableTool{Name: tool.Name, Description: tool.Description, Schema: tool.Schema}
	}
	return out
}

type recordedResponse struct {
	Blocks    []provider.Block `json:"blocks"`
	Reasoning string           `json:"reasoning,omitempty"`
}

func stableResponse(resp *provider.Response) recordedResponse {
	return recordedResponse{Blocks: resp.Blocks, Reasoning: resp.Reasoning}
}

func putObject(tx *sql.Tx, kind string, value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	payload = scrub(payload)
	sum := sha256.Sum256(append(append([]byte(kind), 0), payload...))
	hash := hex.EncodeToString(sum[:])
	_, err = tx.Exec(`INSERT INTO objects(hash, kind, payload, size_bytes, created_at)
		VALUES(?, ?, ?, ?, ?) ON CONFLICT(hash) DO NOTHING`, hash, kind, payload, len(payload), stamp(time.Now().UTC()))
	return hash, err
}

func open(dir string) (*sql.DB, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("trace: create directory: %w", err)
	}
	path := filepath.Join(dir, "trace.sqlite")
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("trace: schema: %w", err)
	}
	if err := migrateSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("trace: migrate schema: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("trace: protect ledger: %w", err)
	}
	return db, nil
}

func migrateSchema(db *sql.DB) error {
	added := false
	columns := map[string][]string{
		"runs": {
			"cache_write_input_tokens INTEGER NOT NULL DEFAULT 0",
			"reasoning_tokens INTEGER NOT NULL DEFAULT 0",
			"total_tokens INTEGER NOT NULL DEFAULT 0",
			"priced_calls INTEGER NOT NULL DEFAULT 0",
			"unpriced_calls INTEGER NOT NULL DEFAULT 0",
			"input_cost_nanos INTEGER NOT NULL DEFAULT 0",
			"cached_cost_nanos INTEGER NOT NULL DEFAULT 0",
			"cache_write_cost_nanos INTEGER NOT NULL DEFAULT 0",
			"output_cost_nanos INTEGER NOT NULL DEFAULT 0",
			"cost_nanos INTEGER NOT NULL DEFAULT 0",
		},
		"calls": {
			"cache_write_input_tokens INTEGER NOT NULL DEFAULT 0",
			"reasoning_tokens INTEGER NOT NULL DEFAULT 0",
			"total_tokens INTEGER NOT NULL DEFAULT 0",
			"pricing_known INTEGER NOT NULL DEFAULT 0",
			"input_price REAL NOT NULL DEFAULT 0",
			"cached_input_price REAL NOT NULL DEFAULT 0",
			"cache_write_price REAL NOT NULL DEFAULT 0",
			"output_price REAL NOT NULL DEFAULT 0",
			"input_cost_nanos INTEGER NOT NULL DEFAULT 0",
			"cached_cost_nanos INTEGER NOT NULL DEFAULT 0",
			"cache_write_cost_nanos INTEGER NOT NULL DEFAULT 0",
			"output_cost_nanos INTEGER NOT NULL DEFAULT 0",
			"cost_nanos INTEGER NOT NULL DEFAULT 0",
		},
	}
	for table, definitions := range columns {
		for _, definition := range definitions {
			name := strings.Fields(definition)[0]
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, name).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				continue
			}
			if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + definition); err != nil {
				if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
					return err
				}
			} else {
				added = true
			}
		}
	}
	if !added {
		return nil
	}
	_, err := db.Exec(`UPDATE runs SET total_tokens = input_tokens + output_tokens WHERE total_tokens = 0 AND (input_tokens != 0 OR output_tokens != 0);
		UPDATE calls SET total_tokens = input_tokens + output_tokens WHERE total_tokens = 0 AND (input_tokens != 0 OR output_tokens != 0)`)
	return err
}

func newRunID(now time.Time) string {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d", now.UnixNano())))
		copy(suffix[:], sum[:])
	}
	return now.Format("20060102T150405.000000Z") + "-" + hex.EncodeToString(suffix[:])
}

func taskOf(req provider.Request) (string, string) {
	for _, message := range req.Messages {
		if message.Role != provider.RoleUser {
			continue
		}
		for _, block := range message.Blocks {
			if block.Type != provider.BlockText || strings.TrimSpace(block.Text) == "" {
				continue
			}
			text := strings.Join(strings.Fields(block.Text), " ")
			sum := sha256.Sum256([]byte(text))
			return hex.EncodeToString(sum[:6]), truncate(scrubText(text), 96)
		}
	}
	return "", ""
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return strings.TrimSpace(value[:limit]) + "..."
}

func stamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

var secretText = regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9._~+/=-]+|(sk|ghp|gho|github_pat|xox[baprs])[-_][A-Za-z0-9._-]{12,}`)
var secretKey = regexp.MustCompile(`(?i)(authorization|api[_-]?key|secret|password|passwd|token|credential|private[_-]?key|bearer)`)

func scrub(raw []byte) []byte {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	clean, err := json.Marshal(scrubValue("", value))
	if err != nil {
		return raw
	}
	return clean
}

func scrubValue(key string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			typed[childKey] = scrubValue(childKey, child)
		}
		return typed
	case []any:
		for i, child := range typed {
			typed[i] = scrubValue(key, child)
		}
		return typed
	case string:
		if key != "" && secretKey.MatchString(key) {
			return "[redacted]"
		}
		return scrubText(typed)
	default:
		return value
	}
}

func scrubText(value string) string {
	return secretText.ReplaceAllStringFunc(value, func(match string) string {
		if strings.HasPrefix(strings.ToLower(match), "bearer ") {
			return match[:7] + "[redacted]"
		}
		return "[redacted]"
	})
}
