package trace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tamnd/tomo/pkg/provider"
)

type fixedProvider struct {
	response *provider.Response
	err      error
}

func (f fixedProvider) Stream(_ context.Context, _ provider.Request, _ func(provider.Event)) (*provider.Response, error) {
	return f.response, f.err
}

func TestTraceDeduplicatesRepeatedContentAndIndexesRun(t *testing.T) {
	dir := t.TempDir()
	base := fixedProvider{response: &provider.Response{
		Blocks:     []provider.Block{provider.Text("answer")},
		StopReason: provider.StopEndTurn,
		Usage:      provider.Usage{InputTokens: 100, CachedInputTokens: 80, OutputTokens: 20},
	}}
	wrapped, err := Wrap(base, Options{Dir: dir, Provider: "zen"})
	if err != nil {
		t.Fatal(err)
	}
	req := provider.Request{
		Model:  "model-free",
		System: "Solve carefully.",
		Messages: []provider.Message{
			provider.UserText("Prove the recurrence."),
		},
		Tools: []provider.Tool{{Name: "check", Description: "check arithmetic", Schema: json.RawMessage(`{"type":"object"}`)}},
	}
	for range 2 {
		if _, err := wrapped.Stream(context.Background(), req, nil); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := Summarize(dir, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Runs != 1 || summary.Calls != 2 || summary.InputTokens != 200 || summary.CachedInputTokens != 160 || summary.OutputTokens != 40 {
		t.Fatalf("summary = %+v", summary)
	}
	// Both calls land in the one run's rollout, so the store holds a single file.
	if summary.StoredRuns != 1 || summary.StoredBytes == 0 {
		t.Fatalf("stored accounting = %+v", summary)
	}
	runs, err := List(dir, Filter{Model: "model-free", Task: "recurrence"})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Provider != "zen" || runs[0].TaskID == "" || runs[0].Status != "complete" {
		t.Fatalf("runs = %+v", runs)
	}
	mode, err := os.Stat(filepath.Join(dir, runs[0].ID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode().Perm() != 0o600 {
		t.Fatalf("rollout mode = %o, want 600", mode.Mode().Perm())
	}
}

func TestTraceRecordsFailuresWithoutChangingProviderResult(t *testing.T) {
	dir := t.TempDir()
	want := errors.New("upstream unavailable")
	wrapped, err := Wrap(fixedProvider{err: want}, Options{Dir: dir, Provider: "local"})
	if err != nil {
		t.Fatal(err)
	}
	_, got := wrapped.Stream(context.Background(), provider.Request{Model: "local-model", Messages: []provider.Message{provider.UserText("task")}}, nil)
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
	summary, err := Summarize(dir, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Runs != 1 || summary.Calls != 1 || summary.FailedCalls != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestTraceScrubsCredentialsAndExportsResolvedRun(t *testing.T) {
	dir := t.TempDir()
	secret := "sk-exampleabcdefghijklmnop"
	wrapped, err := Wrap(fixedProvider{response: &provider.Response{Blocks: []provider.Block{provider.Text("done")}}}, Options{Dir: dir, Provider: "test"})
	if err != nil {
		t.Fatal(err)
	}
	req := provider.Request{Model: "m", Messages: []provider.Message{provider.UserText("use Bearer " + secret)}}
	if _, err := wrapped.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	runs, err := List(dir, Filter{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	path := filepath.Join(t.TempDir(), "run.json")
	if err := ExportNative(dir, runs[0].ID, path); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(secret)) {
		t.Fatal("export contains credential")
	}
	if !strings.Contains(string(payload), "[redacted]") || !json.Valid(payload) {
		t.Fatalf("export is not a valid redacted document: %s", payload)
	}
}

func TestTraceCapturesDetailedUsagePricingAndSTSExport(t *testing.T) {
	dir := t.TempDir()
	base := fixedProvider{response: &provider.Response{
		Blocks: []provider.Block{
			provider.Text("checking"),
			{Type: provider.BlockToolUse, ID: "t1", Name: "check", Input: json.RawMessage(`{"n":2}`)},
		},
		Reasoning:  "checked the invariant",
		StopReason: provider.StopToolUse,
		Usage: provider.Usage{
			InputTokens: 100, CachedInputTokens: 80, CacheWriteInputTokens: 10,
			OutputTokens: 20, ReasoningTokens: 5, TotalTokens: 120,
		},
	}}
	wrapper, err := Wrap(base, Options{
		Dir: dir, Provider: "priced", Pricing: &Pricing{InputUSDPerMillion: 99},
		PricingByModel: map[string]Pricing{"reasoner": {
			InputUSDPerMillion: 10, CachedUSDPerMillion: 1,
			CacheWriteUSDPerMillion: 12.5, OutputUSDPerMillion: 30,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapper.Stream(context.Background(), provider.Request{
		Model: "reasoner", System: "Be exact.", Messages: []provider.Message{provider.UserText("Check two.")},
	}, nil); err != nil {
		t.Fatal(err)
	}
	summary, err := Summarize(dir, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalTokens != 120 || summary.ReasoningTokens != 5 || summary.CacheWriteInputTokens != 10 {
		t.Fatalf("usage summary = %+v", summary)
	}
	if summary.PricedCalls != 1 || summary.UnpricedCalls != 0 || summary.CostNanos != 905000 {
		t.Fatalf("cost summary = %+v", summary)
	}
	runs, err := List(dir, Filter{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %+v, err = %v", runs, err)
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := ExportSTS(dir, runs[0].ID, path); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(payload), []byte("\n"))
	if len(lines) != 4 {
		t.Fatalf("STS lines = %d, want session, system, user, assistant\n%s", len(lines), payload)
	}
	for i, line := range lines {
		if !json.Valid(line) {
			t.Fatalf("STS line %d is invalid JSON: %s", i+1, line)
		}
	}
	if !bytes.Contains(lines[0], []byte(`"harness":"tomo"`)) ||
		!bytes.Contains(lines[0], []byte(`"cost_nanos":905000`)) ||
		!bytes.Contains(lines[3], []byte(`"toolCalls"`)) ||
		!bytes.Contains(lines[3], []byte(`"reasoningContent":"checked the invariant"`)) {
		t.Fatalf("STS export lacks required metadata or messages:\n%s", payload)
	}
}

func TestTraceAllowsConcurrentSessionsWithoutLostCalls(t *testing.T) {
	dir := t.TempDir()
	const sessions = 12
	const callsPerSession = 8
	start := make(chan struct{})
	errs := make(chan error, sessions)
	var wg sync.WaitGroup
	for session := 0; session < sessions; session++ {
		wg.Add(1)
		go func(session int) {
			defer wg.Done()
			wrapper, err := Wrap(fixedProvider{response: &provider.Response{
				Blocks: []provider.Block{provider.Text("ok")},
				Usage:  provider.Usage{InputTokens: 10, OutputTokens: 2},
			}}, Options{Dir: dir, Provider: "parallel", Pricing: &Pricing{}})
			if err != nil {
				errs <- err
				return
			}
			<-start
			for call := 0; call < callsPerSession; call++ {
				_, err := wrapper.Stream(context.Background(), provider.Request{
					Model: "local", Messages: []provider.Message{provider.UserText("shared task")},
				}, nil)
				if err != nil {
					errs <- err
					return
				}
			}
		}(session)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	summary, err := Summarize(dir, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Runs != sessions || summary.Calls != sessions*callsPerSession {
		t.Fatalf("concurrent summary = %+v", summary)
	}
	if summary.PricedCalls != sessions*callsPerSession || summary.CostNanos != 0 {
		t.Fatalf("free pricing summary = %+v", summary)
	}
}

func TestExportDatasetOrganizesSTSFilesWithoutChangingLedger(t *testing.T) {
	root := t.TempDir()
	ledger := filepath.Join(root, "ledger")
	for _, model := range []string{"org/model-a", "model-b"} {
		wrapper, err := Wrap(fixedProvider{response: &provider.Response{
			Blocks: []provider.Block{provider.Text("answer")}, Usage: provider.Usage{InputTokens: 3, OutputTokens: 2},
		}}, Options{Dir: ledger, Provider: "gateway", Pricing: &Pricing{}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wrapper.Stream(context.Background(), provider.Request{
			Model: model, Messages: []provider.Message{provider.UserText("dataset task")},
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(root, "dataset")
	result, err := ExportDataset(ledger, output, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Runs != 2 || result.Bytes == 0 {
		t.Fatalf("dataset result = %+v", result)
	}
	var files []string
	if err := filepath.WalkDir(output, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			files = append(files, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || !strings.Contains(strings.Join(files, "\n"), "org_model-a") {
		t.Fatalf("dataset files = %v", files)
	}
	if _, err := ExportDataset(ledger, filepath.Join(ledger, "exports"), Filter{}); err == nil {
		t.Fatal("dataset export inside ledger should be rejected")
	}
}
