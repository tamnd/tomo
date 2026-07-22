// Package lspregex is a labs A/B that measures whether resolving the context
// pack through a language server, instead of by regex, changes what a real model
// writes.
//
// The mechanism difference is proven deterministically elsewhere
// (pkg/engine/oi/contextpack_lsp_test.go): the regex slicer decides a function's
// extent by counting braces per line, so a brace inside a string literal near the
// top of a function truncates the body; a language server returns the exact
// enclosing range. This lab asks the downstream question: when the truncated
// region is where the bug lives, does the more accurate retrieval let the model
// fix it?
//
// The fixture's Authorize has a "}" in a string on its second line, so the regex
// resolver hands the model a two-line stub that stops before the revocation rule.
// The bug, a revoked credential being authorized in violation of the function's
// own documented rule, is inside the truncated region. The task does not name the
// bug: the model must read the function to find it, so a model handed the stub
// cannot, while a model handed the whole function can.
//
// Both arms make real calls to the same model; they differ only in whether the
// pack was built with regex or with gopls. Grading is not textual: the model's
// returned function is compiled and executed against assertions, so only a fix
// that actually denies a revoked credential counts. Requires gopls in PATH and a
// Go toolchain. Run:
//
//	OPENCODE_API_KEY=... go test ./labs/2026/07/22/23-25-lsp-vs-regex-ab/ -run TestLSPvsRegexLift -v
package lspregex

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/tomo/pkg/engine/oi"
	"github.com/tamnd/tomo/pkg/provider"
)

const task = "The function `Authorize` in this repository has a security bug: it violates a rule that is stated plainly in its own doc comment. " +
	"Read the whole function, find the single line that breaks the documented rule, and correct it. Do not change behaviour for any input the rule does not cover. " +
	"Return only the corrected `Authorize` function in a single ```go code block, with the same signature."

const fixSystem = "You are fixing a bug in a Go repository. Read the context you are given, then write the corrected function. " +
	"Do not explain at length. Return the corrected function in one ```go code block with an unchanged signature."

// gradeSrc is the test that decides whether a returned Authorize is fixed. A
// revoked non-admin must be denied; the unrelated behaviours must be preserved.
const gradeSrc = `package authlib

import "testing"

func TestFix(t *testing.T) {
	if Authorize("admin", nil, false) != true {
		t.Fatal("admin without revocation must be permitted")
	}
	if Authorize("user", []string{"x"}, false) != true {
		t.Fatal("a user with a scope must be permitted")
	}
	if Authorize("user", nil, false) != false {
		t.Fatal("a user with no scope must be denied")
	}
	if Authorize("user", []string{"x"}, true) != false {
		t.Fatal("a revoked user must be denied")
	}
}
`

var goFence = regexp.MustCompile("(?s)```go\\s*(.*?)```")

// extractAuthorize pulls the Authorize function text out of a reply. It parses
// the fenced go block with go/parser and renders the Authorize FuncDecl, so a
// brace inside a string literal cannot truncate it. (An earlier brace-counting
// version had exactly the bug this lab exists to expose, and mangled correct
// fixes into non-compiling code, failing both arms.)
func extractAuthorize(reply string) string {
	body := reply
	if m := goFence.FindStringSubmatch(reply); m != nil {
		body = m[1]
	}
	src := body
	if !strings.Contains(src, "package ") {
		src = "package authlib\n" + src
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "reply.go", src, parser.SkipObjectResolution)
	if err != nil {
		return ""
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "Authorize" {
			continue
		}
		var sb strings.Builder
		if err := printer.Fprint(&sb, fset, fn); err != nil {
			return ""
		}
		return sb.String()
	}
	return ""
}

// gradeCompiles writes the returned function into a throwaway module with the
// grading test and returns whether it compiles and the assertions hold.
func gradeCompiles(t *testing.T, reply string) bool {
	t.Helper()
	fn := extractAuthorize(reply)
	if fn == "" {
		return false
	}
	dir := t.TempDir()
	must := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module authgrade\n\ngo 1.21\n")
	must("authz.go", "package authlib\n\n"+fn+"\n")
	must("authz_test.go", gradeSrc)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("grade: not fixed (%v):\n%s", err, truncate(string(out), 400))
	}
	return err == nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

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
	BaseURL   string      `json:"base_url"`
	Timestamp string      `json:"timestamp"`
	Arms      []armResult `json:"arms"`
}

// TestPremise proves, without any model call, the condition the live A/B rests
// on: the regex pack truncates the function before the revocation rule, so the
// bug is invisible in that arm, while the gopls pack contains it. If this ever
// fails the fixture no longer discriminates and the live numbers mean nothing.
func TestPremise(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not in PATH; cannot build the LSP arm to compare")
	}
	root := fixtureRoot(t)
	regexPack := oi.ContextPack(root, task)
	lspPack := oi.ContextPackWith(root, task, []string{"gopls"})
	const rule = "if revoked {"
	if strings.Contains(regexPack, rule) {
		t.Errorf("regex pack unexpectedly kept the revocation branch; fixture no longer discriminates:\n%s", regexPack)
	}
	if !strings.Contains(lspPack, rule) {
		t.Errorf("lsp pack dropped the revocation branch it must capture:\n%s", lspPack)
	}
	t.Logf("regex pack = %d bytes (truncated), lsp pack = %d bytes (full)", len(regexPack), len(lspPack))
}

// TestGraderExtractsPastBraceInString guards the grader against the very bug the
// lab studies: a returned fix that keeps the `sep := "}"` line must be extracted
// whole and graded on its real behaviour, not truncated at the string brace.
func TestGraderExtractsPastBraceInString(t *testing.T) {
	reply := "Here is the fix:\n\n```go\n" +
		"func Authorize(role string, scopes []string, revoked bool) bool {\n" +
		"\tsep := \"}\"\n\t_ = sep\n" +
		"\tif revoked {\n\t\treturn false\n\t}\n" +
		"\tif role == \"admin\" {\n\t\treturn true\n\t}\n" +
		"\treturn len(scopes) > 0\n}\n```\n"
	fn := extractAuthorize(reply)
	if !strings.Contains(fn, "return len(scopes) > 0") {
		t.Fatalf("extractor truncated the function (brace-in-string bug regressed):\n%s", fn)
	}
	if !gradeCompiles(t, reply) {
		t.Fatal("a correct fix must grade as fixed; grader is broken")
	}
}

func TestLSPvsRegexLift(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY"))
	if apiKey == "" {
		t.Skip("set OPENCODE_API_KEY to run the live LSP-vs-regex A/B")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not in PATH; the LSP arm would fall back to regex and the arms would be identical")
	}
	model := envOr("LAB_MODEL", "deepseek-v4-flash-free")
	baseURL := envOr("LAB_BASE_URL", "https://opencode.ai/zen/v1")
	trials := envInt("LAB_TRIALS", 6)

	root := fixtureRoot(t)
	regexPack := oi.ContextPack(root, task)
	lspPack := oi.ContextPackWith(root, task, []string{"gopls"})
	if regexPack == "" || lspPack == "" {
		t.Fatalf("empty pack(s): regex=%d lsp=%d bytes", len(regexPack), len(lspPack))
	}
	// The premise: the regex pack is shorter because it truncated the function.
	t.Logf("regex pack = %d bytes, lsp pack = %d bytes", len(regexPack), len(lspPack))

	arms := []struct {
		name     string
		preamble string
	}{
		{"regex-pack", regexPack},
		{"lsp-pack", lspPack},
	}
	// LAB_ARM restricts to one arm so the two can be run as independent jobs; the
	// deepseek free tier rambles on the truncated regex stub (~10k-token replies),
	// so running arms in parallel keeps wall-clock down.
	if only := strings.TrimSpace(os.Getenv("LAB_ARM")); only != "" {
		filtered := arms[:0]
		for _, a := range arms {
			if a.name == only {
				filtered = append(filtered, a)
			}
		}
		arms = filtered
		if len(arms) == 0 {
			t.Fatalf("LAB_ARM=%q matched no arm (want regex-pack or lsp-pack)", only)
		}
	}

	prov := &provider.OpenAI{APIKey: apiKey, BaseURL: baseURL}
	var results []armResult
	for _, arm := range arms {
		res := armResult{Name: arm.name, Trials: trials, PreambleLen: len(arm.preamble)}
		userMsg := arm.preamble + "\n\n----\n\n" + task
		for i := 0; i < trials; i++ {
			reply, usage, err := oneCallRetry(t, prov, model, userMsg)
			if err != nil {
				res.Errored++
				t.Logf("[%s trial %d] errored after retries: %v", arm.name, i, err)
				continue
			}
			fixed := gradeCompiles(t, reply)
			if fixed {
				res.Fixed++
			}
			res.PromptToks += usage.InputTokens
			res.OutputToks += usage.OutputTokens
			res.CachedToks += usage.CachedInputTokens
			t.Logf("[%s trial %d] fixed=%v prompt=%d output=%d cached=%d",
				arm.name, i, fixed, usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens)
		}
		graded := res.Trials - res.Errored
		t.Logf("ARM %s: %d/%d fixed (%d errored), preamble=%d bytes, prompt=%d output=%d cached=%d",
			arm.name, res.Fixed, graded, res.Errored, res.PreambleLen, res.PromptToks, res.OutputToks, res.CachedToks)
		results = append(results, res)
	}

	writeReport(t, root, model, abReport{
		Model:     model,
		BaseURL:   baseURL,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Arms:      results,
	})
}

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
		for _, b := range resp.Blocks {
			if b.Type == provider.BlockText {
				text += b.Text
			}
		}
	}
	return text, resp.Usage.Normalize(), nil
}

func writeReport(t *testing.T, root, model string, r abReport) {
	t.Helper()
	dir := filepath.Join(filepath.Dir(root), "results")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(r, "", "  ")
	safe := strings.NewReplacer("/", "_", " ", "_").Replace(model)
	if arm := strings.TrimSpace(os.Getenv("LAB_ARM")); arm != "" {
		safe += "-" + arm
	}
	if err := os.WriteFile(filepath.Join(dir, safe+".json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
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
