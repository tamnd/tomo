// Package trace records model calls as an append-only JSONL rollout, one file
// per run. Each line is a self-contained record — a run header, then one record
// per call carrying its system prompt, tool set, messages, response, usage, and
// priced cost. There is no shared database, no lock, and no checkpoint step, so
// a run's trace is complete the instant its last call returns and survives being
// copied out of a container mid-flight. This is the canonical session store the
// other agents keep (codex, opencode, claude each write their own rollout); tomo
// keeps the same, which is what makes a tomo run reconstructable downstream.
package trace

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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

// runHeader is the first line of a run's rollout: the metadata known when the
// first call is recorded. Everything else about the run (end time, aggregate
// usage, cost, status) is derived by folding the call records, so writing stays
// pure append with no line ever rewritten.
type runHeader struct {
	ID        string `json:"id"`
	RunDate   string `json:"run_date"`
	StartedAt string `json:"started_at"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	TaskID    string `json:"task_id"`
	TaskLabel string `json:"task_label"`
}

// storeLine is one line of a rollout: a tagged union of the run header and a
// call record, so a reader can scan the file with a single decode per line.
type storeLine struct {
	Type string      `json:"type"`
	Run  *runHeader  `json:"run,omitempty"`
	Call *callRecord `json:"call,omitempty"`
}

type tracedProvider struct {
	base     provider.Provider
	opts     Options
	runID    string
	started  time.Time
	sequence atomic.Int64

	mu          sync.Mutex
	file        *os.File
	wroteHeader bool
}

// runPath is where a run's rollout lives: one file named by the run id, so a
// run is a single self-contained artifact and two runs never share a file.
func runPath(dir, id string) string { return filepath.Join(dir, id+".jsonl") }

// Wrap validates the trace directory and returns a transparent provider wrapper
// that appends every call to this run's rollout file.
func Wrap(base provider.Provider, opts Options) (provider.Provider, error) {
	if base == nil {
		return nil, fmt.Errorf("trace: provider is nil")
	}
	if strings.TrimSpace(opts.Dir) == "" {
		return nil, fmt.Errorf("trace: directory is empty")
	}
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("trace: create directory: %w", err)
	}
	now := time.Now().UTC()
	runID := newRunID(now)
	file, err := os.OpenFile(runPath(opts.Dir, runID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("trace: open run log: %w", err)
	}
	return &tracedProvider{base: base, opts: opts, runID: runID, started: now, file: file}, nil
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

// record builds this call's record and appends it (with the run header first, if
// not yet written) to the rollout. A failed call is recorded with its error and
// the provider's own result is never altered.
func (t *tracedProvider) record(req provider.Request, resp *provider.Response, callErr error, sequence int64, started, ended time.Time) error {
	usage := provider.Usage{}
	var response *recordedResponse
	stopReason := ""
	if resp != nil {
		captured := stableResponse(resp)
		response = &captured
		stopReason = resp.StopReason
		usage = resp.Usage.Normalize()
	}
	var errRaw json.RawMessage
	if callErr != nil {
		errRaw, _ = json.Marshal(map[string]string{"message": callErr.Error()})
	}
	pricing := t.opts.Pricing
	if modelPricing, ok := t.opts.PricingByModel[req.Model]; ok {
		pricing = &modelPricing
	}
	cost := priceUsage(usage, pricing)
	rec := callRecord{
		Sequence: sequence, StartedAt: stamp(started), DurationMS: ended.Sub(started).Milliseconds(),
		System: req.System, Tools: stableTools(req.Tools), Messages: req.Messages,
		Response: response, Error: errRaw, StopReason: stopReason,
		InputTokens: usage.InputTokens, CachedInputTokens: usage.CachedInputTokens,
		CacheWriteInputTokens: usage.CacheWriteInputTokens, OutputTokens: usage.OutputTokens,
		ReasoningTokens: usage.ReasoningTokens, TotalTokens: usage.TotalTokens,
		PricingKnown: cost.Known,
		Pricing: Pricing{
			InputUSDPerMillion: cost.InputPrice, CachedUSDPerMillion: cost.CachedPrice,
			CacheWriteUSDPerMillion: cost.CacheWritePrice, OutputUSDPerMillion: cost.OutputPrice,
		},
		InputCostNanos: cost.InputNanos, CachedCostNanos: cost.CachedNanos,
		CacheWriteCostNanos: cost.CacheWriteNanos, OutputCostNanos: cost.OutputNanos,
		CostNanos: cost.TotalNanos, CostUSD: nanosUSD(cost.TotalNanos),
	}
	return t.appendCall(req, rec)
}

func (t *tracedProvider) appendCall(req provider.Request, rec callRecord) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.wroteHeader {
		taskID, taskLabel := taskOf(req)
		header := runHeader{
			ID: t.runID, RunDate: t.started.Format("2006-01-02"), StartedAt: stamp(t.started),
			Provider: t.opts.Provider, Model: req.Model, TaskID: taskID, TaskLabel: taskLabel,
		}
		if err := t.writeLine(storeLine{Type: "run", Run: &header}); err != nil {
			return err
		}
		t.wroteHeader = true
	}
	return t.writeLine(storeLine{Type: "call", Call: &rec})
}

func (t *tracedProvider) writeLine(line storeLine) error {
	payload, err := json.Marshal(line)
	if err != nil {
		return err
	}
	payload = scrub(payload)
	if _, err := t.file.Write(append(payload, '\n')); err != nil {
		return err
	}
	return nil
}

// readRun reads a run's rollout by id, returning its header and calls.
func readRun(dir, runID string) (runHeader, []callRecord, error) {
	file, err := os.Open(runPath(dir, runID))
	if err != nil {
		if os.IsNotExist(err) {
			return runHeader{}, nil, fmt.Errorf("trace run %q not found", runID)
		}
		return runHeader{}, nil, err
	}
	defer file.Close()
	return scanRun(file)
}

// scanRun decodes a rollout stream into its header and call records. A line that
// is neither (a foreign file that merely shares the .jsonl suffix, such as an
// exported STS session) is ignored, so scanning a directory is tolerant.
func scanRun(r io.Reader) (runHeader, []callRecord, error) {
	var header runHeader
	var calls []callRecord
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var line storeLine
		if err := json.Unmarshal(raw, &line); err != nil {
			return header, calls, err
		}
		switch line.Type {
		case "run":
			if line.Run != nil {
				header = *line.Run
			}
		case "call":
			if line.Call != nil {
				line.Call.CostUSD = nanosUSD(line.Call.CostNanos)
				calls = append(calls, *line.Call)
			}
		}
	}
	return header, calls, scanner.Err()
}

// foldRun derives a run's cumulative row from its header and calls: the totals,
// the end time (last call's start plus its duration), and the status, which is
// partial when any call failed and complete otherwise.
func foldRun(header runHeader, calls []callRecord) Run {
	run := Run{
		ID: header.ID, Date: header.RunDate, StartedAt: header.StartedAt, EndedAt: header.StartedAt,
		Provider: header.Provider, Model: header.Model, TaskID: header.TaskID, TaskLabel: header.TaskLabel,
		Status: "complete",
	}
	for _, call := range calls {
		run.Calls++
		if len(call.Error) != 0 {
			run.FailedCalls++
		}
		run.InputTokens += call.InputTokens
		run.CachedInputTokens += call.CachedInputTokens
		run.CacheWriteInputTokens += call.CacheWriteInputTokens
		run.OutputTokens += call.OutputTokens
		run.ReasoningTokens += call.ReasoningTokens
		run.TotalTokens += call.TotalTokens
		if call.PricingKnown {
			run.PricedCalls++
		} else {
			run.UnpricedCalls++
		}
		run.InputCostNanos += call.InputCostNanos
		run.CachedCostNanos += call.CachedCostNanos
		run.CacheWriteCostNanos += call.CacheWriteCostNanos
		run.OutputCostNanos += call.OutputCostNanos
		run.CostNanos += call.CostNanos
		run.DurationMS += call.DurationMS
		if end := advance(call.StartedAt, call.DurationMS); end != "" {
			run.EndedAt = end
		}
	}
	if run.FailedCalls > 0 {
		run.Status = "partial"
	}
	run.CostUSD = nanosUSD(run.CostNanos)
	return run
}

func advance(startedAt string, durationMS int64) string {
	at, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return ""
	}
	return stamp(at.Add(time.Duration(durationMS) * time.Millisecond))
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

func nanosUSD(value int64) float64 { return float64(value) / 1_000_000_000 }

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
