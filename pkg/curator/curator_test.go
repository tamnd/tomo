package curator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/tomo/pkg/memory"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/skill"
)

// scriptProvider replays canned responses and records the requests it saw.
type scriptProvider struct {
	responses []*provider.Response
	reqs      []provider.Request
}

func (s *scriptProvider) Stream(_ context.Context, req provider.Request, _ func(provider.Event)) (*provider.Response, error) {
	s.reqs = append(s.reqs, req)
	resp := s.responses[0]
	s.responses = s.responses[1:]
	return resp, nil
}

func fixedClock() time.Time { return time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC) }

func toolUse(id, name, input string) *provider.Response {
	return &provider.Response{
		Blocks:     []provider.Block{{Type: provider.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}},
		StopReason: provider.StopToolUse,
	}
}

func endTurn(text string) *provider.Response {
	return &provider.Response{Blocks: []provider.Block{provider.Text(text)}, StopReason: provider.StopEndTurn}
}

func TestReflectWritesMemoryWithProvenance(t *testing.T) {
	mem := &memory.Memory{Dir: t.TempDir()}
	sp := &scriptProvider{responses: []*provider.Response{
		toolUse("t1", "memory_write", `{"slug":"coffee","title":"Drinks cortados","body":"Switched to cortados."}`),
		endTurn("done"),
	}}
	c := &Curator{Provider: sp, Model: "m", Memory: mem, Now: fixedClock}

	turn := []provider.Message{
		provider.UserText("I switched from flat whites to cortados last month."),
		{Role: provider.RoleAssistant, Blocks: []provider.Block{provider.Text("Noted, cortados it is.")}},
	}
	if err := c.Reflect(context.Background(), "telegram:42", nil, turn); err != nil {
		t.Fatal(err)
	}

	body, err := mem.Read("coffee")
	if err != nil {
		t.Fatalf("topic not written: %v", err)
	}
	if !strings.Contains(body, "Switched to cortados.") {
		t.Errorf("body = %q", body)
	}
	if !strings.Contains(body, "source: curator, from telegram:42, 2026-07-06") {
		t.Errorf("provenance missing: %q", body)
	}
	// The transcript of the exchange reached the curator.
	if got := blocksText(sp.reqs[0].Messages[0]); !strings.Contains(got, "cortados") {
		t.Errorf("curator did not see the exchange: %q", got)
	}
}

func TestReflectCanWriteNothing(t *testing.T) {
	mem := &memory.Memory{Dir: t.TempDir()}
	sp := &scriptProvider{responses: []*provider.Response{endTurn("nothing durable here")}}
	c := &Curator{Provider: sp, Model: "m", Memory: mem, Now: fixedClock}

	if err := c.Reflect(context.Background(), "web:c", nil, []provider.Message{provider.UserText("thanks!")}); err != nil {
		t.Fatal(err)
	}
	if idx, _ := mem.Index(); idx != "" {
		t.Errorf("a quiet reflection should write no memory, got index %q", idx)
	}
}

func TestReflectDraftsSkillWithoutInstalling(t *testing.T) {
	root := t.TempDir()
	mem := &memory.Memory{Dir: filepath.Join(root, "memory")}
	installed := &skill.Store{Dir: filepath.Join(root, "skills")}
	drafts := &skill.Store{Dir: filepath.Join(root, "drafts")}

	sp := &scriptProvider{responses: []*provider.Response{
		toolUse("s1", "skill_draft", `{"name":"weekly-report","description":"Assemble the Monday report","body":"1. gather commits\n2. summarize\n3. post","permissions":{"read":true,"net":true}}`),
		endTurn("drafted it"),
	}}
	c := &Curator{Provider: sp, Model: "m", Memory: mem, Skills: installed, Drafts: drafts, Now: fixedClock}

	turn := []provider.Message{
		provider.UserText("do my weekly report again"),
		{Role: provider.RoleAssistant, Blocks: []provider.Block{{Type: provider.BlockToolUse, Name: "commits"}}},
	}
	if err := c.Reflect(context.Background(), "web:c", nil, turn); err != nil {
		t.Fatal(err)
	}

	// The draft exists, but nothing was installed: install stays a user act.
	if body, err := drafts.Read("weekly-report"); err != nil || !strings.Contains(body, "gather commits") {
		t.Fatalf("draft = %q %v", body, err)
	}
	if idx, _ := installed.Index(); idx != "" {
		t.Errorf("curator must not install, installed index = %q", idx)
	}
}

func TestNoDraftToolWithoutDraftsStore(t *testing.T) {
	// With no drafts store, the curator only curates memory: a skill_draft call
	// would fail as an unknown tool, so this proves the tool is absent.
	mem := &memory.Memory{Dir: t.TempDir()}
	sp := &scriptProvider{responses: []*provider.Response{endTurn("nothing to do")}}
	c := &Curator{Provider: sp, Model: "m", Memory: mem}
	if err := c.Reflect(context.Background(), "web:c", nil, []provider.Message{provider.UserText("hi")}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.system(), "memory curator") || strings.Contains(c.system(), "draft a skill") {
		t.Errorf("system prompt should omit the drafting clause: %q", c.system())
	}
}

func TestWorthwhile(t *testing.T) {
	// A short toolless exchange is not worth reflecting on.
	short := []provider.Message{
		provider.UserText("thanks"),
		{Role: provider.RoleAssistant, Blocks: []provider.Block{provider.Text("anytime")}},
	}
	if Worthwhile(short) {
		t.Error("a quick chat should not be worthwhile")
	}
	// A turn that reached for a tool is worth it, however short.
	withTool := []provider.Message{
		provider.UserText("what's in my inbox"),
		{Role: provider.RoleAssistant, Blocks: []provider.Block{{Type: provider.BlockToolUse, Name: "inbox"}}},
	}
	if !Worthwhile(withTool) {
		t.Error("a turn using a tool should be worthwhile")
	}
	// A long back-and-forth is worth it even without tools.
	long := []provider.Message{{Role: provider.RoleAssistant, Blocks: []provider.Block{provider.Text(strings.Repeat("x", substantialChars+1))}}}
	if !Worthwhile(long) {
		t.Error("a long exchange should be worthwhile")
	}
}

func blocksText(m provider.Message) string {
	var b strings.Builder
	for _, bl := range m.Blocks {
		if bl.Type == provider.BlockText {
			b.WriteString(bl.Text)
		}
	}
	return b.String()
}
