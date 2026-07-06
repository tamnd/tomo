package telegram

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseCallback(t *testing.T) {
	cases := []struct {
		data      string
		wantTok   string
		wantAllow bool
		wantOK    bool
	}{
		{"abc123|1", "abc123", true, true},
		{"abc123|0", "abc123", false, true},
		{"nopipe", "", false, false},
		{"", "", false, false},
	}
	for _, c := range cases {
		tok, allow, ok := parseCallback(c.data)
		if tok != c.wantTok || allow != c.wantAllow || ok != c.wantOK {
			t.Errorf("parseCallback(%q) = (%q,%v,%v), want (%q,%v,%v)",
				c.data, tok, allow, ok, c.wantTok, c.wantAllow, c.wantOK)
		}
	}
}

func TestSplitMessagePrefersNewline(t *testing.T) {
	text := "line one\nline two\nline three"
	parts := splitMessage(text, 12)
	for _, p := range parts {
		if len(p) > 12 {
			t.Errorf("part %q exceeds max", p)
		}
	}
	if strings.Join(parts, "\n") != text {
		t.Errorf("rejoin = %q, want %q", strings.Join(parts, "\n"), text)
	}
}

func TestSplitMessageShortIsWhole(t *testing.T) {
	parts := splitMessage("hi", 4096)
	if len(parts) != 1 || parts[0] != "hi" {
		t.Errorf("parts = %v", parts)
	}
}

func TestEscapeItalicRemovesUnderscores(t *testing.T) {
	if got := escapeItalic("a_b_c"); got != "a b c" {
		t.Errorf("escapeItalic = %q", got)
	}
}

func TestAllowedChecksList(t *testing.T) {
	tg := &Telegram{Allow: []int64{10, 20}}
	if !tg.allowed(20) {
		t.Error("20 should be allowed")
	}
	if tg.allowed(30) {
		t.Error("30 should not be allowed")
	}
}

func TestGetUpdatesDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "getUpdates") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[
			{"update_id":5,"message":{"text":"hello","chat":{"id":42},"from":{"id":7}}},
			{"update_id":6,"callback_query":{"id":"cq1","data":"tok|1"}}
		]}`))
	}))
	defer srv.Close()

	tg := &Telegram{Token: "x", BaseURL: srv.URL, Client: srv.Client()}
	ups, err := tg.getUpdates(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 2 {
		t.Fatalf("got %d updates", len(ups))
	}
	if ups[0].Message == nil || ups[0].Message.Text != "hello" || ups[0].Message.Chat.ID != 42 || ups[0].Message.From.ID != 7 {
		t.Errorf("message = %+v", ups[0].Message)
	}
	if ups[1].CallbackQuery == nil || ups[1].CallbackQuery.Data != "tok|1" {
		t.Errorf("callback = %+v", ups[1].CallbackQuery)
	}
}

func TestGetUpdatesReportsAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"description":"unauthorized"}`))
	}))
	defer srv.Close()

	tg := &Telegram{Token: "x", BaseURL: srv.URL, Client: srv.Client()}
	if _, err := tg.getUpdates(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("err = %v", err)
	}
}
