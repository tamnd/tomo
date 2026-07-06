package webchat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/channel"
)

func TestServeChatStreamsSSE(t *testing.T) {
	w := &WebChat{}
	w.handler = func(_ context.Context, x channel.Exchange) {
		if x.In.Text != "ping" {
			t.Errorf("inbound text = %q", x.In.Text)
		}
		x.Reply.Notice("· time")
		x.Reply.Chunk("pong")
		x.Reply.Done()
	}

	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"session":"s1","text":"ping"}`))
	rec := httptest.NewRecorder()
	w.serveChat(rec, req)

	body := rec.Body.String()
	for _, want := range []string{`"type":"notice"`, `"type":"chunk"`, `"text":"pong"`, `"type":"done"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}
}

func TestServeChatDefaultsSession(t *testing.T) {
	w := &WebChat{}
	var got string
	w.handler = func(_ context.Context, x channel.Exchange) {
		got = x.In.Chat
		x.Reply.Done()
	}
	req := httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(`{"text":"hi"}`))
	w.serveChat(httptest.NewRecorder(), req)
	if got != "web" {
		t.Errorf("default session = %q, want web", got)
	}
}

func TestServeApproveResolvesToken(t *testing.T) {
	w := &WebChat{}
	ch := make(chan bool, 1)
	w.pending.Store("tok1", ch)

	req := httptest.NewRequest(http.MethodPost, "/api/approve", strings.NewReader(`{"token":"tok1","allow":true}`))
	rec := httptest.NewRecorder()
	w.serveApprove(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d", rec.Code)
	}
	select {
	case allow := <-ch:
		if !allow {
			t.Error("expected allow=true")
		}
	default:
		t.Error("token was not resolved")
	}
}

func TestServeIndexServesPage(t *testing.T) {
	w := &WebChat{}
	rec := httptest.NewRecorder()
	w.serveIndex(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tomo") {
		t.Error("index page does not mention tomo")
	}
}
