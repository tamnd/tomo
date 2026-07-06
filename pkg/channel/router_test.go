package channel

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/store"
	"github.com/tamnd/tomo/pkg/tool"
)

// scriptProvider replays canned responses.
type scriptProvider struct {
	mu        sync.Mutex
	responses []*provider.Response
}

func (s *scriptProvider) Stream(_ context.Context, _ provider.Request, emit func(provider.Event)) (*provider.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := s.responses[0]
	s.responses = s.responses[1:]
	if emit != nil {
		for _, b := range resp.Blocks {
			if b.Type == provider.BlockText {
				emit(provider.Event{Type: provider.EventText, Text: b.Text})
			}
		}
	}
	return resp, nil
}

// captureReply records everything the router sent back.
type captureReply struct {
	chunks  []string
	notices []string
	done    bool
}

func (c *captureReply) Chunk(t string)  { c.chunks = append(c.chunks, t) }
func (c *captureReply) Notice(t string) { c.notices = append(c.notices, t) }
func (c *captureReply) Done()           { c.done = true }

func (c *captureReply) text() string { return strings.Join(c.chunks, "") }

// yesApprover approves everything.
type yesApprover struct{ asked int }

func (y *yesApprover) Approve(_ context.Context, _ policy.Request) (bool, error) {
	y.asked++
	return true, nil
}

func newTestRouter(t *testing.T, resp []*provider.Response, tools ...tool.Tool) (*Router, *scriptProvider, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sp := &scriptProvider{responses: resp}
	reg := tool.NewRegistry(tools...)
	newAgent := func() (*agent.Agent, error) {
		return &agent.Agent{Provider: sp, Model: "m", Tools: reg, MaxTurns: 8}, nil
	}
	return NewRouter(st, policy.New(policy.Config{}), nil, newAgent), sp, st
}

func TestRouterRunsTurnAndPersists(t *testing.T) {
	r, _, st := newTestRouter(t, []*provider.Response{
		{Blocks: []provider.Block{provider.Text("hi there")}, StopReason: provider.StopEndTurn},
	})
	rep := &captureReply{}
	r.HandlerFor("web")(context.Background(), Exchange{
		In:       Inbound{Chat: "c1", User: "u", Text: "hello"},
		Reply:    rep,
		Approver: &yesApprover{},
	})

	if rep.text() != "hi there" || !rep.done {
		t.Errorf("reply = %q done=%v", rep.text(), rep.done)
	}
	// The session key is channel:chat, and the turn was persisted.
	sess, err := st.Session("web:c1", "web")
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := st.Messages(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Blocks[0].Text != "hello" {
		t.Errorf("persisted = %+v", msgs)
	}
}

func TestRouterHistoryCarriesAcrossMessages(t *testing.T) {
	r, _, _ := newTestRouter(t, []*provider.Response{
		{Blocks: []provider.Block{provider.Text("first")}, StopReason: provider.StopEndTurn},
		{Blocks: []provider.Block{provider.Text("second")}, StopReason: provider.StopEndTurn},
	})
	h := r.HandlerFor("web")

	h(context.Background(), Exchange{In: Inbound{Chat: "c", Text: "one"}, Reply: &captureReply{}, Approver: &yesApprover{}})
	rep := &captureReply{}
	h(context.Background(), Exchange{In: Inbound{Chat: "c", Text: "two"}, Reply: rep, Approver: &yesApprover{}})
	if rep.text() != "second" {
		t.Errorf("second turn = %q", rep.text())
	}
}

func TestRouterApprovalAndToolNotice(t *testing.T) {
	writeTool := tool.Tool{
		Name: "write_file", Class: tool.ClassWrite,
		Schema: json.RawMessage(`{"type":"object"}`),
		Run:    func(context.Context, json.RawMessage) (string, error) { return "wrote", nil },
	}
	r, _, _ := newTestRouter(t, []*provider.Response{
		{Blocks: []provider.Block{{Type: provider.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"x"}`)}}, StopReason: provider.StopToolUse},
		{Blocks: []provider.Block{provider.Text("saved it")}, StopReason: provider.StopEndTurn},
	}, writeTool)

	ap := &yesApprover{}
	rep := &captureReply{}
	r.HandlerFor("web")(context.Background(), Exchange{
		In: Inbound{Chat: "c", Text: "save x"}, Reply: rep, Approver: ap,
	})

	if ap.asked != 1 {
		t.Errorf("write should have prompted once, asked=%d", ap.asked)
	}
	if rep.text() != "saved it" {
		t.Errorf("final text = %q", rep.text())
	}
	// A tool run emits a notice line naming the tool.
	joined := strings.Join(rep.notices, "\n")
	if !strings.Contains(joined, "write_file") {
		t.Errorf("notices = %v", rep.notices)
	}
}

func TestSessionCommandBindsChat(t *testing.T) {
	r, _, st := newTestRouter(t, nil)
	rep := &captureReply{}
	r.HandlerFor("web")(context.Background(), Exchange{
		In: Inbound{Chat: "c1", Text: "/session work"}, Reply: rep, Approver: &yesApprover{},
	})
	if !rep.done {
		t.Error("command should finalize the reply")
	}
	if !strings.Contains(strings.Join(rep.notices, "\n"), "work") {
		t.Errorf("notices = %v", rep.notices)
	}
	name, ok, err := st.BindingFor("web", "c1")
	if err != nil || !ok || name != "work" {
		t.Errorf("binding = %q ok=%v err=%v", name, ok, err)
	}
}

func TestSharedSessionAcrossChannels(t *testing.T) {
	r, _, st := newTestRouter(t, []*provider.Response{
		{Blocks: []provider.Block{provider.Text("from telegram")}, StopReason: provider.StopEndTurn},
		{Blocks: []provider.Block{provider.Text("from web")}, StopReason: provider.StopEndTurn},
	})

	// Bind a telegram chat and a web chat to the same session name.
	r.HandlerFor("telegram")(context.Background(), Exchange{In: Inbound{Chat: "tg1", Text: "/session shared"}, Reply: &captureReply{}, Approver: &yesApprover{}})
	r.HandlerFor("web")(context.Background(), Exchange{In: Inbound{Chat: "web1", Text: "/session shared"}, Reply: &captureReply{}, Approver: &yesApprover{}})

	// A message from each channel now lands in the same "shared" session.
	r.HandlerFor("telegram")(context.Background(), Exchange{In: Inbound{Chat: "tg1", User: "u_tg", Text: "hi from phone"}, Reply: &captureReply{}, Approver: &yesApprover{}})
	r.HandlerFor("web")(context.Background(), Exchange{In: Inbound{Chat: "web1", User: "u_web", Text: "hi from browser"}, Reply: &captureReply{}, Approver: &yesApprover{}})

	sess, err := st.Session("shared", "telegram")
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := st.Messages(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("shared session has %d messages, want 4: %+v", len(msgs), msgs)
	}
	if msgs[0].Blocks[0].Text != "hi from phone" || msgs[2].Blocks[0].Text != "hi from browser" {
		t.Errorf("ledger order = %+v", msgs)
	}
}

func TestInboundMessageOrdersTextThenImages(t *testing.T) {
	in := Inbound{Text: "look", Images: []provider.Block{{Type: provider.BlockImage, MediaType: "image/png", Data: "aGk="}}}
	m := in.Message()
	if len(m.Blocks) != 2 || m.Blocks[0].Type != provider.BlockText || m.Blocks[1].Type != provider.BlockImage {
		t.Errorf("message blocks = %+v", m.Blocks)
	}
}
