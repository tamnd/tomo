// Command migrate-legacy-traces imports tomo-labs proxy captures into tomo's
// normalized trace ledger. It is intentionally separate from the shipped CLI.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"

	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/trace"
)

type report struct {
	StartedAt          string  `json:"started_at"`
	FinishedAt         string  `json:"finished_at"`
	Source             string  `json:"source"`
	Destination        string  `json:"destination"`
	SourceRuns         int     `json:"source_runs"`
	SourceCalls        int     `json:"source_calls"`
	ImportedRuns       int     `json:"imported_runs"`
	ImportedCalls      int     `json:"imported_calls"`
	ExistingRuns       int     `json:"existing_runs"`
	SkippedRequests    int     `json:"skipped_non_model_requests"`
	MissingResponses   int     `json:"missing_responses"`
	SourceFiles        int     `json:"source_files"`
	SourceBytes        int64   `json:"source_bytes"`
	SourceManifestSHA  string  `json:"source_manifest_sha256"`
	LedgerBytes        int64   `json:"ledger_bytes"`
	LogicalBytesSaved  int64   `json:"logical_bytes_saved"`
	LogicalSavingsPct  float64 `json:"logical_savings_percent"`
	PhysicalBytesAdded int64   `json:"physical_bytes_added"`
	VerifiedRuns       int     `json:"verified_runs"`
	VerifiedCalls      int     `json:"verified_calls"`
	UniqueObjects      int     `json:"unique_objects"`
	ObjectBytes        int64   `json:"object_bytes"`
	IntegrityCheck     string  `json:"integrity_check"`
}

type sourceRun struct {
	dir      string
	rel      string
	provider string
	calls    []trace.ImportCall
	skipped  int
	missing  int
}

type requestLine struct {
	Seq    int             `json:"seq"`
	TS     string          `json:"ts"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
}

type usageLine struct {
	Seq              int `json:"seq"`
	Status           int `json:"status"`
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type latencyLine struct {
	Seq     int   `json:"seq"`
	Status  int   `json:"status"`
	TotalMS int64 `json:"total_ms"`
}

type minimalConfig struct {
	DefaultModel string `yaml:"default_model"`
}

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fatal(err)
	}
	source := flag.String("source", filepath.Join(home, "data"), "root containing legacy trace directories")
	destination := flag.String("destination", filepath.Join(home, "data", "tomo-traces-v2"), "new normalized trace directory")
	dryRun := flag.Bool("dry-run", false, "inventory and parse without writing the ledger")
	flag.Parse()

	started := time.Now().UTC()
	r := report{StartedAt: started.Format(time.RFC3339Nano), Source: *source, Destination: *destination}
	manifest := sha256.New()
	dirs, err := discover(*source)
	if err != nil {
		fatal(err)
	}
	for index, dir := range dirs {
		run, files, bytesRead, err := parseRun(*source, dir, manifest)
		if err != nil {
			fatal(fmt.Errorf("%s: %w", dir, err))
		}
		r.SourceFiles += files
		r.SourceBytes += bytesRead
		if len(run.calls) > 0 {
			r.SourceRuns++
		}
		r.SourceCalls += len(run.calls)
		r.SkippedRequests += run.skipped
		r.MissingResponses += run.missing
		if !*dryRun && len(run.calls) > 0 {
			id := deterministicID(run.rel)
			pricing := pricingFor(run.calls[0].Request.Model, run.provider)
			result, err := trace.ImportHistoricalRun(context.Background(), *destination, trace.ImportRun{
				ID: id, StartedAt: run.calls[0].StartedAt, Provider: run.provider,
				Pricing: pricing, Calls: run.calls,
			})
			if err != nil {
				fatal(err)
			}
			if result.AlreadyComplete {
				r.ExistingRuns++
			} else {
				r.ImportedRuns++
				r.ImportedCalls += result.ImportedCalls
			}
		}
		if (index+1)%25 == 0 || index+1 == len(dirs) {
			fmt.Fprintf(os.Stderr, "parsed %d/%d runs, %d calls\n", index+1, len(dirs), r.SourceCalls)
		}
	}
	r.SourceManifestSHA = hex.EncodeToString(manifest.Sum(nil))
	if !*dryRun {
		if err := checkpointAndVerify(*destination, &r); err != nil {
			fatal(err)
		}
		r.LedgerBytes, err = traceStoreBytes(*destination)
		if err != nil {
			fatal(err)
		}
		r.PhysicalBytesAdded = r.LedgerBytes
		r.LogicalBytesSaved = r.SourceBytes - r.LedgerBytes
		if r.SourceBytes > 0 {
			r.LogicalSavingsPct = 100 * float64(r.LogicalBytesSaved) / float64(r.SourceBytes)
		}
	}
	r.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	payload, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		fatal(err)
	}
	payload = append(payload, '\n')
	if !*dryRun {
		path := filepath.Join(*destination, "migration-report.json")
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			fatal(err)
		}
	}
	if _, err := os.Stdout.Write(payload); err != nil {
		fatal(err)
	}
}

func discover(root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && path != root && (entry.Name() == "tomo-traces-v2" || entry.Name() == ".git") {
			return filepath.SkipDir
		}
		if !entry.IsDir() && entry.Name() == "config.yaml" && filepath.Base(filepath.Dir(path)) == "trace" {
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "requests.jsonl")); err == nil {
				dirs = append(dirs, filepath.Dir(path))
			}
		}
		return nil
	})
	sort.Strings(dirs)
	return dirs, err
}

func parseRun(root, dir string, manifest io.Writer) (sourceRun, int, int64, error) {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return sourceRun{}, 0, 0, err
	}
	run := sourceRun{dir: dir, rel: rel, provider: providerName(filepath.Join(dir, "config.yaml"))}
	usage := map[int]usageLine{}
	latency := map[int]latencyLine{}
	if err := readJSONL(filepath.Join(dir, "usage.jsonl"), func(raw []byte) error {
		var item usageLine
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		usage[item.Seq] = item
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return run, 0, 0, err
	}
	if err := readJSONL(filepath.Join(dir, "latency.jsonl"), func(raw []byte) error {
		var item latencyLine
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		latency[item.Seq] = item
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return run, 0, 0, err
	}
	err = readJSONL(filepath.Join(dir, "requests.jsonl"), func(raw []byte) error {
		var item requestLine
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		if item.Method != "POST" || !strings.Contains(item.Path, "chat/completions") || len(item.Body) == 0 {
			run.skipped++
			return nil
		}
		req, err := parseRequest(item.Body)
		if err != nil {
			return fmt.Errorf("request sequence %d: %w", item.Seq, err)
		}
		started, err := time.Parse(time.RFC3339Nano, item.TS)
		if err != nil {
			return err
		}
		respPath := filepath.Join(dir, "resp-"+strconv.Itoa(item.Seq)+".txt")
		resp, responseErr := parseResponse(respPath)
		if responseErr != nil && !os.IsNotExist(responseErr) {
			return responseErr
		}
		missingResponse := responseErr != nil
		if missingResponse {
			run.missing++
		}
		if details, ok := usage[item.Seq]; ok {
			if resp == nil {
				resp = &provider.Response{}
			}
			resp.Usage.InputTokens = details.PromptTokens
			resp.Usage.OutputTokens = details.CompletionTokens
			resp.Usage.TotalTokens = details.TotalTokens
			resp.Usage = resp.Usage.Normalize()
		}
		duration := time.Duration(latency[item.Seq].TotalMS) * time.Millisecond
		call := trace.ImportCall{StartedAt: started, Duration: duration, Request: req, Response: resp}
		if missingResponse {
			call.Error = "legacy response file missing"
		}
		if details, ok := usage[item.Seq]; ok && details.Status >= 400 {
			call.Error = fmt.Sprintf("legacy proxy status %d", details.Status)
		}
		run.calls = append(run.calls, call)
		return nil
	})
	if err != nil {
		return run, 0, 0, err
	}
	files, bytesRead, err := hashInputs(dir, rel, manifest)
	return run, files, bytesRead, err
}

func parseRequest(raw []byte) (provider.Request, error) {
	var body struct {
		Model    string            `json:"model"`
		Messages []json.RawMessage `json:"messages"`
		Tools    []struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return provider.Request{}, err
	}
	out := provider.Request{Model: body.Model}
	for _, tool := range body.Tools {
		out.Tools = append(out.Tools, provider.Tool{Name: tool.Function.Name, Description: tool.Function.Description, Schema: tool.Function.Parameters})
	}
	for _, rawMessage := range body.Messages {
		messages, system, err := parseWireMessage(rawMessage)
		if err != nil {
			return out, err
		}
		if system != "" {
			if out.System != "" {
				out.System += "\n"
			}
			out.System += system
		}
		out.Messages = append(out.Messages, messages...)
	}
	return out, nil
}

func parseWireMessage(raw json.RawMessage) ([]provider.Message, string, error) {
	var message struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil, "", err
	}
	content := textContent(message.Content)
	switch message.Role {
	case "system", "developer":
		return nil, content, nil
	case "tool":
		return []provider.Message{{Role: provider.RoleUser, Blocks: []provider.Block{{Type: provider.BlockToolResult, ToolID: message.ToolCallID, Content: content}}}}, "", nil
	case "assistant":
		out := provider.Message{Role: provider.RoleAssistant}
		if content != "" {
			out.Blocks = append(out.Blocks, provider.Text(content))
		}
		for _, call := range message.ToolCalls {
			args := json.RawMessage(call.Function.Arguments)
			if !json.Valid(args) {
				args = json.RawMessage("{}")
			}
			out.Blocks = append(out.Blocks, provider.Block{Type: provider.BlockToolUse, ID: call.ID, Name: call.Function.Name, Input: args})
		}
		return []provider.Message{out}, "", nil
	default:
		return []provider.Message{{Role: provider.RoleUser, Blocks: []provider.Block{provider.Text(content)}}}, "", nil
	}
}

func textContent(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return string(raw)
	}
	var out strings.Builder
	for _, part := range parts {
		if part.Text != "" {
			out.WriteString(part.Text)
		}
		if part.ImageURL.URL != "" {
			out.WriteString(part.ImageURL.URL)
		}
	}
	return out.String()
}

func parseResponse(path string) (*provider.Response, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	out := &provider.Response{StopReason: provider.StopEndTurn}
	var content, reasoning strings.Builder
	type pendingCall struct {
		id, name string
		args     strings.Builder
	}
	calls := map[int]*pendingCall{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if bytes.Equal(data, []byte("[DONE]")) || len(data) == 0 {
			continue
		}
		var chunk struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content          string `json:"content"`
					Reasoning        string `json:"reasoning"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
				PromptDetails    *struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
				CompletionDetails *struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"completion_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(data, &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			out.Usage.InputTokens = chunk.Usage.PromptTokens
			out.Usage.OutputTokens = chunk.Usage.CompletionTokens
			out.Usage.TotalTokens = chunk.Usage.TotalTokens
			if chunk.Usage.PromptDetails != nil {
				out.Usage.CachedInputTokens = chunk.Usage.PromptDetails.CachedTokens
			}
			if chunk.Usage.CompletionDetails != nil {
				out.Usage.ReasoningTokens = chunk.Usage.CompletionDetails.ReasoningTokens
			}
		}
		for _, choice := range chunk.Choices {
			content.WriteString(choice.Delta.Content)
			reasoning.WriteString(choice.Delta.Reasoning)
			reasoning.WriteString(choice.Delta.ReasoningContent)
			for _, call := range choice.Delta.ToolCalls {
				pending := calls[call.Index]
				if pending == nil {
					pending = &pendingCall{}
					calls[call.Index] = pending
				}
				if call.ID != "" {
					pending.id = call.ID
				}
				if call.Function.Name != "" {
					pending.name = call.Function.Name
				}
				pending.args.WriteString(call.Function.Arguments)
			}
			switch choice.FinishReason {
			case "tool_calls", "function_call":
				out.StopReason = provider.StopToolUse
			case "length":
				out.StopReason = provider.StopMaxTokens
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if content.Len() > 0 {
		out.Blocks = append(out.Blocks, provider.Text(content.String()))
		out.Reasoning = reasoning.String()
	} else if reasoning.Len() > 0 && len(calls) == 0 {
		out.Blocks = append(out.Blocks, provider.Text(reasoning.String()))
	}
	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		call := calls[index]
		args := json.RawMessage(call.args.String())
		if !json.Valid(args) {
			args = json.RawMessage("{}")
		}
		out.Blocks = append(out.Blocks, provider.Block{Type: provider.BlockToolUse, ID: call.id, Name: call.name, Input: args})
	}
	if len(calls) > 0 {
		out.StopReason = provider.StopToolUse
	}
	out.Usage = out.Usage.Normalize()
	return out, nil
}

func readJSONL(path string, visit func([]byte) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 128<<20)
	for scanner.Scan() {
		if err := visit(scanner.Bytes()); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func providerName(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "legacy"
	}
	var cfg minimalConfig
	if yaml.Unmarshal(raw, &cfg) != nil {
		return "legacy"
	}
	name, _, ok := strings.Cut(cfg.DefaultModel, "/")
	if !ok || name == "" {
		return "legacy"
	}
	return name
}

func pricingFor(model, providerName string) *trace.Pricing {
	rates := map[string]trace.Pricing{
		"gpt-5.6-sol":   {InputUSDPerMillion: 5, CachedUSDPerMillion: .5, OutputUSDPerMillion: 30},
		"gpt-5.6-terra": {InputUSDPerMillion: 2.5, CachedUSDPerMillion: .25, OutputUSDPerMillion: 15},
		"gpt-5.6-luna":  {InputUSDPerMillion: 1, CachedUSDPerMillion: .1, OutputUSDPerMillion: 6},
		"gpt-5.5":       {InputUSDPerMillion: 5, CachedUSDPerMillion: .5, OutputUSDPerMillion: 30},
		"gpt-5.4":       {InputUSDPerMillion: 2.5, CachedUSDPerMillion: .25, OutputUSDPerMillion: 15},
		"gpt-5.4-mini":  {InputUSDPerMillion: .75, CachedUSDPerMillion: .075, OutputUSDPerMillion: 4.5},
	}
	if pricing, ok := rates[model]; ok {
		return &pricing
	}
	if strings.HasSuffix(model, "-free") || providerName == "local" || providerName == "ollama" {
		return &trace.Pricing{}
	}
	return nil
}

func hashInputs(dir, rel string, manifest io.Writer) (int, int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "config.yaml" || name == "requests.jsonl" || name == "usage.jsonl" ||
			name == "latency.jsonl" || strings.HasPrefix(name, "resp-") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var total int64
	for _, name := range names {
		path := filepath.Join(dir, name)
		file, err := os.Open(path)
		if err != nil {
			return 0, total, err
		}
		hash := sha256.New()
		n, err := io.Copy(hash, file)
		closeErr := file.Close()
		if err != nil {
			return 0, total, err
		}
		if closeErr != nil {
			return 0, total, closeErr
		}
		total += n
		fmt.Fprintf(manifest, "%s\t%d\t%s\n", filepath.Join(rel, name), n, hex.EncodeToString(hash.Sum(nil)))
	}
	return len(names), total, nil
}

func deterministicID(relative string) string {
	sum := sha256.Sum256([]byte(filepath.ToSlash(relative)))
	return "legacy-" + hex.EncodeToString(sum[:12])
}

func checkpointAndVerify(dir string, r *report) error {
	path := filepath.Join(dir, "trace.sqlite")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE); VACUUM`); err != nil {
		return err
	}
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&r.IntegrityCheck); err != nil {
		return err
	}
	if r.IntegrityCheck != "ok" {
		return fmt.Errorf("ledger integrity check: %s", r.IntegrityCheck)
	}
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(calls),0) FROM runs WHERE id LIKE 'legacy-%'`).Scan(&r.VerifiedRuns, &r.VerifiedCalls); err != nil {
		return err
	}
	if r.VerifiedRuns != r.SourceRuns || r.VerifiedCalls != r.SourceCalls {
		return fmt.Errorf("verification mismatch: source %d runs/%d calls, ledger %d runs/%d calls", r.SourceRuns, r.SourceCalls, r.VerifiedRuns, r.VerifiedCalls)
	}
	return db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size_bytes),0) FROM objects`).Scan(&r.UniqueObjects, &r.ObjectBytes)
}

func traceStoreBytes(dir string) (int64, error) {
	var total int64
	for _, name := range []string{"trace.sqlite", "trace.sqlite-wal", "trace.sqlite-shm"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "migrate-legacy-traces:", err)
	os.Exit(1)
}
