//go:build darwin

package imessage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tamnd/tomo/pkg/channel"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "chat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
		CREATE TABLE handle (ROWID INTEGER PRIMARY KEY, id TEXT);
		CREATE TABLE message (ROWID INTEGER PRIMARY KEY, text TEXT, handle_id INTEGER, is_from_me INTEGER);
		INSERT INTO handle (ROWID, id) VALUES (1, '+15550001'), (2, '+15550002');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPollReturnsInboundOnly(t *testing.T) {
	db := testDB(t)
	_, err := db.Exec(`INSERT INTO message (ROWID, text, handle_id, is_from_me) VALUES
		(10, 'hello', 1, 0),
		(11, 'my own reply', 1, 1),
		(12, NULL, 2, 0),
		(13, 'second', 2, 0)`)
	if err != nil {
		t.Fatal(err)
	}

	msgs, last, err := poll(context.Background(), db, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Text != "hello" || msgs[0].Handle != "+15550001" {
		t.Errorf("msg0 = %+v", msgs[0])
	}
	if msgs[1].Text != "second" || msgs[1].Handle != "+15550002" {
		t.Errorf("msg1 = %+v", msgs[1])
	}
	if last != 13 {
		t.Errorf("high-water = %d, want 13", last)
	}
}

func TestPollAdvancesWatermark(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO message (ROWID, text, handle_id, is_from_me) VALUES (5, 'old', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	msgs, last, err := poll(context.Background(), db, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 || last != 5 {
		t.Errorf("expected nothing past watermark, got %d msgs last=%d", len(msgs), last)
	}
}

func TestMaxRowIDEmpty(t *testing.T) {
	db := testDB(t)
	got, err := maxRowID(context.Background(), db)
	if err != nil || got != 0 {
		t.Errorf("maxRowID on empty = %d, %v", got, err)
	}
}

func TestIsYes(t *testing.T) {
	for _, s := range []string{"yes", "Y", " ok ", "Allow", "sure"} {
		if !isYes(s) {
			t.Errorf("isYes(%q) = false", s)
		}
	}
	for _, s := range []string{"no", "nope", "stop", ""} {
		if isYes(s) {
			t.Errorf("isYes(%q) = true", s)
		}
	}
}

func TestAllowedRequiresListing(t *testing.T) {
	m := &IMessage{Allow: []string{"+15550001"}}
	if !m.allowed("+15550001") {
		t.Error("listed handle should be allowed")
	}
	if m.allowed("+15559999") {
		t.Error("unlisted handle should be denied")
	}
	empty := &IMessage{}
	if empty.allowed("+15550001") {
		t.Error("empty allowlist should serve nobody")
	}
}

func TestOnMessagePendingApprovalIntercepts(t *testing.T) {
	m := &IMessage{Allow: []string{"+15550001"}}
	var turns int
	m.handler = func(context.Context, channel.Exchange) { turns++ }

	ch := make(chan bool, 1)
	m.pending.Store("+15550001", ch)
	m.onMessage(context.Background(), imsg{Handle: "+15550001", Text: "yes"})

	select {
	case allow := <-ch:
		if !allow {
			t.Error("expected the yes reply to allow")
		}
	default:
		t.Fatal("pending approval was not resolved")
	}
	time.Sleep(20 * time.Millisecond)
	if turns != 0 {
		t.Errorf("a pending-approval reply must not start a turn, got %d", turns)
	}
}

func TestOnMessageUnlistedIgnored(t *testing.T) {
	m := &IMessage{Allow: []string{"+15550001"}}
	var turns int
	m.handler = func(context.Context, channel.Exchange) { turns++ }
	m.onMessage(context.Background(), imsg{Handle: "+15559999", Text: "hi"})
	time.Sleep(20 * time.Millisecond)
	if turns != 0 {
		t.Errorf("unlisted handle started %d turns", turns)
	}
}

func TestReplyDeliversOnDone(t *testing.T) {
	var sent []string
	m := &IMessage{send: func(_, text string) error { sent = append(sent, text); return nil }}
	r := &imReply{m: m, ctx: context.Background(), handle: "+15550001"}
	r.Chunk("part one ")
	r.Chunk("part two")
	r.Done()
	if len(sent) != 1 || sent[0] != "part one part two" {
		t.Errorf("sent = %v", sent)
	}
}
