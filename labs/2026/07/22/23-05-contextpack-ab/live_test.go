// Package contextpack is a labs A/B that measures whether the oi engine's
// symbol-anchored context pack changes what a real model writes.
//
// The question. The engine change was made because a code-as-action model read a
// file and still edited the wrong branch of the function the task named: it never
// put the deciding lines in front of itself. The pack is deterministic retrieval
// that lifts the named identifiers to their full definitions and hands them to
// the model once before the loop. Does that focused retrieval actually lift the
// fix rate, or is it decoration?
//
// The design isolates the retrieval variable and nothing else. Both arms get the
// same system instruction, the same task, and a real call to the same model. They
// differ only in the context they are handed:
//
//   - whole-repo: every source file in the fixture, concatenated, the way a model
//     that dumped the tree would see it. This arm is handed strictly more raw text
//     than the pack arm, distractors included.
//   - pack: only oi.ContextPack(root, task), the resolved definitions of the
//     identifiers the task names.
//
// If the pack arm wins while seeing less text, the win is focused retrieval, not
// more context. The fixture buries the deciding branch the way the real task did:
// the module-path branch of load_settings returns without loading the
// environment-named companion, while the file-path branch loads it. A correct fix
// loads a second, companion module inside the module-path branch, so the grader
// counts import_module calls: the base code has one, a real fix has two.
//
// This is not mocked. It calls the free deepseek model on the opencode zen proxy
// first, per the cheap-before-expensive ladder; set LAB_MODEL to escalate. Run:
//
//	OPENCODE_API_KEY=... go test ./labs/contextpack/ -run TestContextPackLift -v
package contextpack

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/tomo/pkg/engine/oi"
	"github.com/tamnd/tomo/pkg/provider"
)

// task names the identifier and the defect in general terms. It does not name the
// fixture's answer, the file to edit, or the companion's exact spelling: the
// model has to find where load_settings goes wrong and mirror the file-path
// branch's companion loading into the module-path branch.
const task = "In this repository, `load_settings` must layer an environment-named companion on top of the base values for BOTH kinds of source. " +
	"Today it does this only when the source is a file path; when the source is a dotted module path it loads the base module and returns without ever loading the environment-named companion module. " +
	"Fix `load_settings` so that a dotted module source also loads its environment-named companion module and lets those values override the base ones. " +
	"Return only the corrected `load_settings` function in a single ```python code block."

// vagueTask mirrors the terseness of the real eval's checklist item ("settings
// loader must load multiple environments"). It does NOT say which branch is
// broken: the model must notice on its own that the file-path branch already
// loads the companion and the module-path branch does not. This is the inference
// the real agent failed, so it is the fairer test of whether focused retrieval
// helps a model see the deciding branch.
const vagueTask = "In this repository, `load_settings` must support loading multiple environments: after loading a source's base values it should also load the environment-named companion for that source and let the companion's values override the base ones. " +
	"Make `load_settings` do this for every kind of source it accepts. " +
	"Return only the corrected `load_settings` function in a single ```python code block."

const fixSystem = "You are fixing a bug in a Python repository. " +
	"Read the context you are given, then write the corrected function. " +
	"Do not explain at length. Return the corrected function in one ```python code block."

// gradeImportModule returns how many import_module calls the reply's code makes.
// The base fixture makes exactly one; a fix that loads the companion module in the
// module-path branch makes two. This is the structural discriminator between a
// real fix and code that only restates the base.
var importModuleRe = regexp.MustCompile(`import_module\s*\(`)

func gradeFixed(reply string) bool {
	return len(importModuleRe.FindAllString(reply, -1)) >= 2
}

// TestPackResolvesDecidingBranch is the deterministic half: no model, no key. It
// proves the pack surfaces the full definition including the module-path branch
// that returns early, which is the branch a fix must change. If this fails the
// live A/B is meaningless, so it guards the mechanism first.
func TestPackResolvesDecidingBranch(t *testing.T) {
	root := fixtureRoot(t)
	pack := oi.ContextPack(root, task)
	if pack == "" {
		t.Fatal("context pack was empty for a task that names load_settings")
	}
	for _, want := range []string{
		"load_settings",              // the named symbol resolved
		"if is_module_path(source):", // the module-path branch is present
		"return store",               // the early return the fix must move past
		"companion = f\"{env.lower()}_{source}\"", // the file-branch companion the module branch lacks
	} {
		if !strings.Contains(pack, want) {
			t.Errorf("pack missing %q; the deciding lines were not surfaced:\n%s", want, pack)
		}
	}
	t.Logf("pack is %d bytes and includes the full load_settings definition", len(pack))
}

// armResult accumulates one arm's outcome across trials.
type armResult struct {
	Name        string `json:"arm"`
	Trials      int    `json:"trials"`
	Fixed       int    `json:"fixed"`
	Errored     int    `json:"errored"`
	PromptToks  int    `json:"prompt_tokens"`
	OutputToks  int    `json:"output_tokens"`
	CachedToks  int    `json:"cached_input_tokens"`
	PreambleLen int    `json:"preamble_bytes"`
}

type abReport struct {
	Model     string      `json:"model"`
	Task      string      `json:"task"`
	BaseURL   string      `json:"base_url"`
	Timestamp string      `json:"timestamp"`
	Arms      []armResult `json:"arms"`
}

func TestContextPackLift(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live context-pack A/B")
	}
	model := envOr("LAB_MODEL", "deepseek-v4-flash-free")
	baseURL := envOr("LAB_BASE_URL", "https://opencode.ai/zen/v1")
	trials := envInt("LAB_TRIALS", 6)

	taskText := task
	if strings.EqualFold(envOr("LAB_TASK", "explicit"), "vague") {
		taskText = vagueTask
	}
	root := fixtureRoot(t)
	wholeRepo := concatSources(t, root)
	pack := oi.ContextPack(root, taskText)
	if pack == "" {
		t.Fatal("empty pack; TestPackResolvesDecidingBranch should have caught this")
	}

	arms := []struct {
		name     string
		preamble string
	}{
		{"whole-repo", "Repository source files:\n\n" + wholeRepo},
		{"pack", pack},
	}

	prov := &provider.OpenAI{APIKey: apiKey, BaseURL: baseURL}
	var results []armResult
	for _, arm := range arms {
		res := armResult{Name: arm.name, Trials: trials, PreambleLen: len(arm.preamble)}
		userMsg := arm.preamble + "\n\n----\n\n" + taskText
		for i := 0; i < trials; i++ {
			reply, usage, err := oneCallRetry(t, prov, model, userMsg)
			if err != nil {
				// A free endpoint returns the occasional 500. Record the trial as
				// errored rather than aborting the whole A/B: an unstable upstream is
				// the harness's problem, not a verdict on the model or the pack.
				res.Errored++
				t.Logf("[%s trial %d] errored after retries: %v", arm.name, i, err)
				continue
			}
			if gradeFixed(reply) {
				res.Fixed++
			}
			res.PromptToks += usage.InputTokens
			res.OutputToks += usage.OutputTokens
			res.CachedToks += usage.CachedInputTokens
			t.Logf("[%s trial %d] fixed=%v prompt=%d output=%d cached=%d",
				arm.name, i, gradeFixed(reply), usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens)
		}
		graded := res.Trials - res.Errored
		t.Logf("ARM %s: %d/%d fixed (%d errored), preamble=%d bytes, prompt=%d output=%d cached=%d tokens total",
			arm.name, res.Fixed, graded, res.Errored, res.PreambleLen, res.PromptToks, res.OutputToks, res.CachedToks)
		results = append(results, res)
	}

	variant := envOr("LAB_TASK", "explicit")
	writeReport(t, root, model, variant, abReport{
		Model:     model,
		Task:      variant,
		BaseURL:   baseURL,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Arms:      results,
	})
}

// oneCallRetry retries a call a few times on any error, since the free endpoints
// return the occasional transient 500, with a short backoff between attempts.
func oneCallRetry(t *testing.T, prov provider.Provider, model, user string) (reply string, usage provider.Usage, err error) {
	t.Helper()
	for attempt := 0; attempt < 4; attempt++ {
		reply, usage, err = oneCall(t, prov, model, user)
		if err == nil {
			return reply, usage, nil
		}
		time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
	}
	return "", provider.Usage{}, err
}

func oneCall(t *testing.T, prov provider.Provider, model, user string) (string, provider.Usage, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var sb strings.Builder
	resp, err := prov.Stream(ctx, provider.Request{
		Model:    model,
		System:   fixSystem,
		Messages: []provider.Message{provider.UserText(user)},
	}, func(ev provider.Event) {
		if ev.Type == provider.EventText {
			sb.WriteString(ev.Text)
		}
	})
	if err != nil {
		return "", provider.Usage{}, err
	}
	text := sb.String()
	if text == "" && resp != nil {
		// Some providers deliver the whole body at once rather than as text events.
		for _, b := range resp.Blocks {
			if b.Type == provider.BlockText {
				text += b.Text
			}
		}
	}
	return text, resp.Usage.Normalize(), nil
}

// concatSources joins every Python file under root, the realistic "I read the
// whole repo" view, distractors and all.
func concatSources(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".py" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		fmt.Fprintf(&b, "# ===== %s =====\n%s\n\n", rel, data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func writeReport(t *testing.T, root, model, variant string, r abReport) {
	t.Helper()
	// results/ sits next to the fixture, under the lab dir. The file is named per
	// model and task variant so runs do not overwrite each other.
	dir := filepath.Join(filepath.Dir(root), "results")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(r, "", "  ")
	safe := strings.NewReplacer("/", "_", " ", "_").Replace(model + "-" + variant)
	out := filepath.Join(dir, safe+".json")
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", out)
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate lab source")
	}
	return filepath.Join(filepath.Dir(file), "fixture")
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func envInt(name string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return fallback
	}
	return n
}
