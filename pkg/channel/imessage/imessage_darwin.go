//go:build darwin

// Package imessage is tomo's macOS iMessage channel. Inbound messages are read
// from the Messages database (~/Library/Messages/chat.db), which needs Full
// Disk Access; outbound messages are sent by driving Messages.app with
// AppleScript. iMessage has no buttons, so approvals degrade to a plain
// question the user answers with yes or no in the same thread.
package imessage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tamnd/tomo/pkg/channel"
	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/provider"
)

// IMessage watches the Messages database and replies through Messages.app.
type IMessage struct {
	Allow  []string      // handles (phone/email) permitted to talk to the agent
	DBPath string        // chat.db path; empty means the default location
	Poll   time.Duration // how often to check for new messages

	// send delivers text to a handle. Nil uses AppleScript via osascript;
	// tests override it.
	send func(handle, text string) error

	handler channel.Handler
	pending sync.Map // chat handle -> chan bool
}

// Name implements channel.Channel.
func (m *IMessage) Name() string { return "imessage" }

// Caps implements channel.Channel. iMessage carries attachments but has no
// buttons and no incremental editing, so approvals fall back to text.
func (m *IMessage) Caps() channel.Caps { return channel.Caps{Media: true} }

func (m *IMessage) allowed(handle string) bool {
	// An empty allowlist serves nobody; iMessage reaches a real person's
	// account, so it must be opt-in per contact.
	return slices.Contains(m.Allow, handle)
}

func (m *IMessage) dbPath() (string, error) {
	if m.DBPath != "" {
		return m.DBPath, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Messages", "chat.db"), nil
}

func (m *IMessage) interval() time.Duration {
	if m.Poll > 0 {
		return m.Poll
	}
	return 2 * time.Second
}

// Run polls the database until ctx is cancelled. It starts from the newest
// message so history is not replayed on startup.
func (m *IMessage) Run(ctx context.Context, h channel.Handler) error {
	m.handler = h
	path, err := m.dbPath()
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()

	last, err := maxRowID(ctx, db)
	if err != nil {
		return fmt.Errorf("imessage: read chat.db (is Full Disk Access granted?): %w", err)
	}

	t := time.NewTicker(m.interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			msgs, newLast, err := poll(ctx, db, last)
			if err != nil {
				continue // transient; try again next tick
			}
			last = newLast
			for _, msg := range msgs {
				m.onMessage(ctx, msg)
			}
		}
	}
}

func (m *IMessage) onMessage(ctx context.Context, msg imsg) {
	if !m.allowed(msg.Handle) {
		return
	}
	// A pending approval turns the next reply in that thread into a yes/no
	// answer instead of a new turn.
	if ch, loaded := m.pending.LoadAndDelete(msg.Handle); loaded {
		ch.(chan bool) <- isYes(msg.Text)
		return
	}
	reply := &imReply{m: m, ctx: ctx, handle: msg.Handle}
	x := channel.Exchange{
		In:       channel.Inbound{Chat: msg.Handle, User: msg.Handle, Text: msg.Text, Images: msg.Images},
		Reply:    reply,
		Approver: &imApprover{m: m, ctx: ctx, handle: msg.Handle},
	}
	go m.handler(ctx, x)
}

// Post pushes a message to a handle outside a reply, for scheduled runs. It
// implements schedule.Poster.
func (m *IMessage) Post(_ context.Context, chat, text string) error {
	return m.deliver(chat, text)
}

func (m *IMessage) deliver(handle, text string) error {
	if m.send != nil {
		return m.send(handle, text)
	}
	return sendAppleScript(handle, text)
}

// imReply buffers streamed text and sends it whole on Done. Notices go out as
// their own message.
type imReply struct {
	m      *IMessage
	ctx    context.Context
	handle string
	buf    strings.Builder
}

func (r *imReply) Chunk(text string) { r.buf.WriteString(text) }

func (r *imReply) Notice(text string) { _ = r.m.deliver(r.handle, text) }

func (r *imReply) Done() {
	text := strings.TrimSpace(r.buf.String())
	if text == "" {
		return
	}
	_ = r.m.deliver(r.handle, text)
}

// imApprover asks the question as text and waits for the next reply in the
// thread, which the watcher routes back here as yes or no.
type imApprover struct {
	m      *IMessage
	ctx    context.Context
	handle string
}

func (a *imApprover) Approve(_ context.Context, req policy.Request) (bool, error) {
	ch := make(chan bool, 1)
	a.m.pending.Store(a.handle, ch)
	defer a.m.pending.Delete(a.handle)

	q := fmt.Sprintf("tomo wants to run %s [%s]. %s\nReply yes to allow or no to deny.", req.Tool, req.Class, req.Reason)
	if err := a.m.deliver(a.handle, q); err != nil {
		return false, err
	}

	select {
	case allow := <-ch:
		return allow, nil
	case <-a.ctx.Done():
		return false, a.ctx.Err()
	case <-time.After(5 * time.Minute):
		return false, nil
	}
}

// imsg is one inbound row.
type imsg struct {
	RowID  int64
	Handle string
	Text   string
	Images []provider.Block
}

func maxRowID(ctx context.Context, db *sql.DB) (int64, error) {
	var max sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(ROWID) FROM message`).Scan(&max); err != nil {
		return 0, err
	}
	return max.Int64, nil
}

// poll returns inbound messages newer than after, and the new high-water rowid.
// A message counts as inbound if it carries text or an image attachment; our
// own sends are skipped. The watermark advances past every message we saw, so a
// text-less, image-less row is not re-examined on the next tick.
func poll(ctx context.Context, db *sql.DB, after int64) ([]imsg, int64, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT m.ROWID, h.id, COALESCE(m.text, '')
		FROM message m
		JOIN handle h ON h.ROWID = m.handle_id
		WHERE m.ROWID > ? AND m.is_from_me = 0
		ORDER BY m.ROWID ASC`, after)
	if err != nil {
		return nil, after, err
	}

	last := after
	var seen []imsg
	for rows.Next() {
		var msg imsg
		if err := rows.Scan(&msg.RowID, &msg.Handle, &msg.Text); err != nil {
			rows.Close()
			return nil, after, err
		}
		seen = append(seen, msg)
		last = msg.RowID
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, after, err
	}
	rows.Close()

	var out []imsg
	for _, msg := range seen {
		// Attachments are best effort: a missing table or unreadable file
		// leaves the message as text-only rather than dropping the turn.
		if imgs, err := attachments(ctx, db, msg.RowID); err == nil {
			msg.Images = imgs
		}
		if msg.Text == "" && len(msg.Images) == 0 {
			continue
		}
		out = append(out, msg)
	}
	return out, last, nil
}

// attachments returns the image attachments on a message as model-ready blocks.
// Non-image files are ignored; iMessage keeps each attachment as a file on disk
// under the user's home, so this reads them straight off the filesystem.
func attachments(ctx context.Context, db *sql.DB, msgID int64) ([]provider.Block, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT a.filename, COALESCE(a.mime_type, '')
		FROM attachment a
		JOIN message_attachment_join maj ON maj.attachment_id = a.ROWID
		WHERE maj.message_id = ?`, msgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []provider.Block
	for rows.Next() {
		var path, mime string
		if err := rows.Scan(&path, &mime); err != nil {
			return nil, err
		}
		if img, err := channel.ReadImageFile(expandHome(path), mime); err == nil {
			out = append(out, img)
		}
	}
	return out, rows.Err()
}

// expandHome resolves a leading ~ the way chat.db records attachment paths.
func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}

func isYes(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "y", "yes", "yeah", "yep", "ok", "okay", "allow", "sure":
		return true
	default:
		return false
	}
}

func sendAppleScript(handle, text string) error {
	script := fmt.Sprintf(`tell application "Messages"
	set targetService to 1st account whose service type = iMessage
	set targetBuddy to participant %q of targetService
	send %q to targetBuddy
end tell`, handle, text)
	return exec.Command("osascript", "-e", script).Run()
}
