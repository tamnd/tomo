package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/tamnd/tomo/pkg/channel"
)

func TestParseValue(t *testing.T) {
	tok, allow, ok := parseValue("abcd|1")
	if tok != "abcd" || !allow || !ok {
		t.Errorf("parseValue allow = (%q,%v,%v)", tok, allow, ok)
	}
	if _, _, ok := parseValue("nopipe"); ok {
		t.Error("expected not ok on missing pipe")
	}
}

func TestAllowedChecksList(t *testing.T) {
	s := &Slack{Allow: []string{"C1"}}
	if !s.allowed("C1") || s.allowed("C2") {
		t.Error("allow list not honored")
	}
}

func TestOnEventSkipsBotAndSubtype(t *testing.T) {
	var called int
	s := &Slack{Allow: []string{"C1"}}
	s.handler = func(context.Context, channel.Exchange) { called++ }

	s.onEvent(context.Background(), json.RawMessage(`{"event":{"type":"message","channel":"C1","text":"hi","bot_id":"B1"}}`))
	s.onEvent(context.Background(), json.RawMessage(`{"event":{"type":"message","channel":"C1","text":"hi","subtype":"message_changed"}}`))
	s.onEvent(context.Background(), json.RawMessage(`{"event":{"type":"message","channel":"C9","text":"hi"}}`))
	time.Sleep(20 * time.Millisecond)
	if called != 0 {
		t.Errorf("handler ran %d times, want 0", called)
	}
}

func TestOnInteractiveResolvesPending(t *testing.T) {
	s := &Slack{}
	ch := make(chan bool, 1)
	s.pending.Store("tok", ch)
	s.onInteractive(context.Background(), json.RawMessage(`{"type":"block_actions","actions":[{"value":"tok|0"}]}`))
	select {
	case allow := <-ch:
		if allow {
			t.Error("expected deny")
		}
	default:
		t.Error("pending not resolved")
	}
}

// A socket-mode session against an in-process fake: it opens the connection,
// reads the events_api envelope, acks it, and dispatches the message.
func TestSessionAcksAndDispatches(t *testing.T) {
	acked := make(chan string, 1)
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		_ = wsjson.Write(ctx, conn, map[string]any{"type": "hello"})
		_ = wsjson.Write(ctx, conn, map[string]any{
			"type":        "events_api",
			"envelope_id": "env-1",
			"payload":     map[string]any{"event": map[string]any{"type": "message", "channel": "C1", "user": "U9", "text": "ping"}},
		})
		var ack map[string]any
		if err := wsjson.Read(ctx, conn, &ack); err == nil {
			if id, _ := ack["envelope_id"].(string); id != "" {
				acked <- id
			}
		}
		<-ctx.Done()
	}))
	defer ws.Close()

	// The web API server answers apps.connections.open with the ws URL.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": "ws" + strings.TrimPrefix(ws.URL, "http")})
	}))
	defer api.Close()

	got := make(chan channel.Inbound, 1)
	s := &Slack{AppToken: "xapp", BotToken: "xoxb", Allow: []string{"C1"}, BaseURL: api.URL}
	s.handler = func(_ context.Context, x channel.Exchange) { got <- x.In }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() { _ = s.session(ctx) })

	select {
	case id := <-acked:
		if id != "env-1" {
			t.Errorf("acked envelope = %q", id)
		}
	case <-ctx.Done():
		t.Fatal("envelope was not acked")
	}
	select {
	case in := <-got:
		if in.Chat != "C1" || in.Text != "ping" || in.User != "U9" {
			t.Errorf("inbound = %+v", in)
		}
	case <-ctx.Done():
		t.Fatal("message not dispatched")
	}
	cancel()
	wg.Wait()
}

func TestOpenConnectionReportsError(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
	}))
	defer api.Close()

	s := &Slack{AppToken: "xapp", BaseURL: api.URL}
	if _, err := s.openConnection(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid_auth") {
		t.Errorf("err = %v", err)
	}
}
