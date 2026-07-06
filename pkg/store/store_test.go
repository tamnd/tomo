package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/tamnd/tomo/pkg/provider"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSessionRoundTrip(t *testing.T) {
	s := open(t)

	sess, err := s.Session("daily", "terminal")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Session("daily", "terminal")
	if err != nil || again.ID != sess.ID {
		t.Fatalf("get-or-create not stable: %v vs %v (%v)", sess, again, err)
	}

	turn := []provider.Message{
		provider.UserText("check uptime"),
		{Role: provider.RoleAssistant, Blocks: []provider.Block{
			{Type: provider.BlockToolUse, ID: "t1", Name: "shell", Input: json.RawMessage(`{"command":"uptime"}`)},
		}},
		{Role: provider.RoleUser, Blocks: []provider.Block{
			{Type: provider.BlockToolResult, ToolID: "t1", Content: "up 3 days", IsError: false},
		}},
		{Role: provider.RoleAssistant, Blocks: []provider.Block{provider.Text("Up 3 days.")}},
	}
	if err := s.Append(sess.ID, turn); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(sess.ID, []provider.Message{provider.UserText("thanks")}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Messages(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("messages = %d, want 5", len(got))
	}
	tu := got[1].Blocks[0]
	if tu.Type != provider.BlockToolUse || tu.Name != "shell" || string(tu.Input) != `{"command":"uptime"}` {
		t.Errorf("tool_use lost in round trip: %+v", tu)
	}
	if got[4].Blocks[0].Text != "thanks" {
		t.Errorf("ordering broken: %+v", got[4])
	}
}

func TestSessionsList(t *testing.T) {
	s := open(t)
	a, _ := s.Session("a", "terminal")
	if _, err := s.Session("b", "telegram"); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(a.ID, []provider.Message{provider.UserText("hi")}); err != nil {
		t.Fatal(err)
	}

	list, err := s.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("sessions = %d", len(list))
	}
	byName := map[string]Session{}
	for _, sess := range list {
		byName[sess.Name] = sess
	}
	if byName["a"].Messages != 1 || byName["b"].Messages != 0 {
		t.Errorf("counts = a:%d b:%d", byName["a"].Messages, byName["b"].Messages)
	}
	if byName["b"].Channel != "telegram" {
		t.Errorf("channel = %q", byName["b"].Channel)
	}
}
