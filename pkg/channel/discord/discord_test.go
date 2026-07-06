package discord

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

func TestParseCustom(t *testing.T) {
	cases := []struct {
		in    string
		tok   string
		allow bool
		ok    bool
	}{
		{"deadbeef|1", "deadbeef", true, true},
		{"deadbeef|0", "deadbeef", false, true},
		{"nopipe", "", false, false},
	}
	for _, c := range cases {
		tok, allow, ok := parseCustom(c.in)
		if tok != c.tok || allow != c.allow || ok != c.ok {
			t.Errorf("parseCustom(%q) = (%q,%v,%v)", c.in, tok, allow, ok)
		}
	}
}

func TestSplitMessagePrefersNewline(t *testing.T) {
	text := "aaaa\nbbbb\ncccc"
	parts := splitMessage(text, 6)
	for _, p := range parts {
		if len(p) > 6 {
			t.Errorf("part %q too long", p)
		}
	}
	if strings.Join(parts, "\n") != text {
		t.Errorf("rejoin = %q", strings.Join(parts, "\n"))
	}
}

func TestAllowedChecksList(t *testing.T) {
	d := &Discord{Allow: []string{"c1", "c2"}}
	if !d.allowed("c2") || d.allowed("c3") {
		t.Error("allow list not honored")
	}
}

func TestOnMessageIgnoresBotAndUnlisted(t *testing.T) {
	var called int
	d := &Discord{Allow: []string{"good"}}
	d.handler = func(context.Context, channel.Exchange) { called++ }

	d.onMessage(context.Background(), json.RawMessage(`{"channel_id":"good","content":"hi","author":{"id":"1","bot":true}}`))
	d.onMessage(context.Background(), json.RawMessage(`{"channel_id":"bad","content":"hi","author":{"id":"1"}}`))
	d.onMessage(context.Background(), json.RawMessage(`{"channel_id":"good","content":"","author":{"id":"1"}}`))
	time.Sleep(20 * time.Millisecond)
	if called != 0 {
		t.Errorf("handler ran %d times, want 0", called)
	}
}

// A full gateway handshake against an in-process fake: hello, identify, one
// message dispatch. Proves the session loop drives the SPI.
func TestSessionHandshakeDispatchesMessage(t *testing.T) {
	// Fake gateway: send Hello, read Identify, push a MESSAGE_CREATE.
	identified := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		_ = wsjson.Write(ctx, conn, map[string]any{"op": opHello, "d": map[string]any{"heartbeat_interval": 45000}})

		var idf gwPayload
		if err := wsjson.Read(ctx, conn, &idf); err != nil {
			return
		}
		var d map[string]any
		_ = json.Unmarshal(idf.D, &d)
		identified <- d

		_ = wsjson.Write(ctx, conn, map[string]any{
			"op": opDispatch, "t": "MESSAGE_CREATE", "s": 1,
			"d": map[string]any{"channel_id": "good", "content": "hello there", "author": map[string]any{"id": "42"}},
		})
		<-ctx.Done()
	}))
	defer srv.Close()

	got := make(chan channel.Inbound, 1)
	d := &Discord{
		Token:      "t",
		Allow:      []string{"good"},
		GatewayURL: "ws" + strings.TrimPrefix(srv.URL, "http"),
	}
	d.handler = func(_ context.Context, x channel.Exchange) { got <- x.In }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() { _ = d.session(ctx) })

	select {
	case idf := <-identified:
		if idf["token"] != "t" {
			t.Errorf("identify token = %v", idf["token"])
		}
	case <-ctx.Done():
		t.Fatal("never received identify")
	}

	select {
	case in := <-got:
		if in.Chat != "good" || in.Text != "hello there" || in.User != "42" {
			t.Errorf("inbound = %+v", in)
		}
	case <-ctx.Done():
		t.Fatal("message was not dispatched")
	}
	cancel()
	wg.Wait()
}

func TestOnInteractionResolvesPending(t *testing.T) {
	// REST callback ack lands here; we only care it is attempted, not its body.
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer rest.Close()

	d := &Discord{Token: "t", BaseURL: rest.URL}
	ch := make(chan bool, 1)
	d.pending.Store("tok", ch)

	d.onInteraction(context.Background(), json.RawMessage(`{"id":"i1","token":"itok","type":3,"data":{"custom_id":"tok|1"}}`))

	select {
	case allow := <-ch:
		if !allow {
			t.Error("expected allow=true")
		}
	default:
		t.Error("pending approval not resolved")
	}
}

// tiny valid PNG served by the fake CDN in the image test.
var onePixelPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func TestOnMessageIngestsImageAttachment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(onePixelPNG)
	}))
	defer srv.Close()

	d := &Discord{Allow: []string{"c1"}}
	got := make(chan channel.Inbound, 1)
	d.handler = func(_ context.Context, x channel.Exchange) { got <- x.In }

	payload := `{"channel_id":"c1","author":{"id":"u1"},"content":"look at this",` +
		`"attachments":[{"url":"` + srv.URL + `/a.png","content_type":"image/png"},` +
		`{"url":"` + srv.URL + `/doc.pdf","content_type":"application/pdf"}]}`
	d.onMessage(context.Background(), json.RawMessage(payload))

	select {
	case in := <-got:
		if in.Text != "look at this" {
			t.Errorf("text = %q", in.Text)
		}
		if len(in.Images) != 1 {
			t.Errorf("images = %d, want 1 (pdf skipped)", len(in.Images))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran")
	}
}

func TestOnMessageSkipsEmptyAndBots(t *testing.T) {
	d := &Discord{Allow: []string{"c1"}}
	var mu sync.Mutex
	var turns int
	d.handler = func(_ context.Context, _ channel.Exchange) {
		mu.Lock()
		turns++
		mu.Unlock()
	}
	// A bot message and an empty message both do nothing.
	d.onMessage(context.Background(), json.RawMessage(`{"channel_id":"c1","author":{"id":"b","bot":true},"content":"hi"}`))
	d.onMessage(context.Background(), json.RawMessage(`{"channel_id":"c1","author":{"id":"u1"},"content":""}`))
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if turns != 0 {
		t.Errorf("started %d turns, want 0", turns)
	}
}

func TestOnMessageIngestsAudioAttachment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/ogg")
		_, _ = w.Write([]byte("OggS-voice"))
	}))
	defer srv.Close()

	d := &Discord{Allow: []string{"c1"}}
	got := make(chan channel.Inbound, 1)
	d.handler = func(_ context.Context, x channel.Exchange) { got <- x.In }

	payload := `{"channel_id":"c1","author":{"id":"u1"},"content":"",` +
		`"attachments":[{"url":"` + srv.URL + `/note.ogg","content_type":"audio/ogg"}]}`
	d.onMessage(context.Background(), json.RawMessage(payload))

	select {
	case in := <-got:
		if len(in.Audio) != 1 {
			t.Errorf("audio clips = %d, want 1", len(in.Audio))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran")
	}
}
