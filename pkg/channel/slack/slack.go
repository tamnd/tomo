// Package slack is tomo's Slack channel. It runs in socket mode, so there is
// no public request URL to host: an app-level token opens a websocket, events
// arrive over it, and each is acknowledged by its envelope id. Messages are
// sent with a bot token over the web API. Only allowlisted channels are served,
// and approvals render as Block Kit buttons.
package slack

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/tamnd/tomo/pkg/channel"
	"github.com/tamnd/tomo/pkg/policy"
)

// Slack serves an allowlisted set of channels over a socket-mode connection.
type Slack struct {
	AppToken string   // xapp-... , opens the socket
	BotToken string   // xoxb-... , posts messages
	Allow    []string // channel ids permitted to talk to the bot
	BaseURL  string   // web API base, default https://slack.com/api
	Client   *http.Client

	// Dial connects the socket. Nil uses websocket.Dial; tests override it.
	Dial func(ctx context.Context, url string) (*websocket.Conn, error)

	handler channel.Handler
	pending sync.Map // token -> chan bool
}

// Name implements channel.Channel.
func (s *Slack) Name() string { return "slack" }

// Caps implements channel.Channel. Slack carries files and renders buttons.
func (s *Slack) Caps() channel.Caps { return channel.Caps{Media: true, Buttons: true} }

func (s *Slack) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *Slack) apiBase() string {
	if s.BaseURL != "" {
		return strings.TrimSuffix(s.BaseURL, "/")
	}
	return "https://slack.com/api"
}

func (s *Slack) allowed(channelID string) bool {
	return slices.Contains(s.Allow, channelID)
}

// Post pushes a message to a channel outside a reply, for scheduled runs. It
// implements channel.Poster.
func (s *Slack) Post(ctx context.Context, chat, text string) error {
	for _, part := range splitMessage(text, 3800) {
		if err := s.post(ctx, map[string]any{"channel": chat, "text": part}); err != nil {
			return err
		}
	}
	return nil
}

// Run opens a socket, serves it until it drops, and reconnects with a short
// backoff, until ctx is cancelled.
func (s *Slack) Run(ctx context.Context, h channel.Handler) error {
	s.handler = h
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := s.session(ctx); err != nil && ctx.Err() == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
		}
	}
}

func (s *Slack) session(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	url, err := s.openConnection(ctx)
	if err != nil {
		return err
	}
	conn, err := s.dial(ctx, url)
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	sess := &sockSession{conn: conn}
	for {
		var env envelope
		if err := wsjson.Read(ctx, conn, &env); err != nil {
			return err
		}
		// Acknowledge anything that carries an envelope id, right away, so
		// Slack does not retry the delivery.
		if env.EnvelopeID != "" {
			if err := sess.ack(ctx, env.EnvelopeID); err != nil {
				return err
			}
		}
		switch env.Type {
		case "disconnect":
			return fmt.Errorf("slack: server asked to reconnect")
		case "events_api":
			s.onEvent(ctx, env.Payload)
		case "interactive":
			s.onInteractive(ctx, env.Payload)
		}
	}
}

func (s *Slack) onEvent(ctx context.Context, payload json.RawMessage) {
	var p struct {
		Event struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Channel string `json:"channel"`
			User    string `json:"user"`
			Text    string `json:"text"`
			BotID   string `json:"bot_id"`
		} `json:"event"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return
	}
	e := p.Event
	// Only plain user messages: skip edits and joins (subtype) and anything a
	// bot posted (bot_id), which includes our own replies.
	if e.Type != "message" || e.Subtype != "" || e.BotID != "" || e.Text == "" || !s.allowed(e.Channel) {
		return
	}
	reply := &slReply{s: s, ctx: ctx, channelID: e.Channel}
	x := channel.Exchange{
		In:       channel.Inbound{Chat: e.Channel, User: e.User, Text: e.Text},
		Reply:    reply,
		Approver: &slApprover{s: s, ctx: ctx, channelID: e.Channel},
	}
	go s.handler(ctx, x)
}

func (s *Slack) onInteractive(_ context.Context, payload json.RawMessage) {
	var p struct {
		Type    string `json:"type"`
		Actions []struct {
			Value string `json:"value"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(payload, &p); err != nil || p.Type != "block_actions" || len(p.Actions) == 0 {
		return
	}
	token, allow, ok := parseValue(p.Actions[0].Value)
	if !ok {
		return
	}
	if ch, loaded := s.pending.LoadAndDelete(token); loaded {
		ch.(chan bool) <- allow
	}
}

// sockSession serializes writes; the ack path shares the socket with nothing
// else here, but wrapping it keeps the one-writer rule explicit.
type sockSession struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (s *sockSession) ack(ctx context.Context, envelopeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return wsjson.Write(ctx, s.conn, map[string]any{"envelope_id": envelopeID})
}

// slReply buffers streamed text and posts it whole on Done. Slack messages can
// be long, but keeping to a comfortable size avoids truncation surprises.
type slReply struct {
	s         *Slack
	ctx       context.Context
	channelID string
	buf       strings.Builder
}

func (r *slReply) Chunk(text string) { r.buf.WriteString(text) }

func (r *slReply) Notice(text string) {
	_ = r.s.post(r.ctx, map[string]any{"channel": r.channelID, "text": "_" + escapeItalic(text) + "_"})
}

func (r *slReply) Done() {
	text := strings.TrimSpace(r.buf.String())
	if text == "" {
		return
	}
	for _, part := range splitMessage(text, 3800) {
		_ = r.s.post(r.ctx, map[string]any{"channel": r.channelID, "text": part})
	}
}

// slApprover posts a Block Kit message with Allow/Deny buttons and waits.
type slApprover struct {
	s         *Slack
	ctx       context.Context
	channelID string
}

func (a *slApprover) Approve(_ context.Context, req policy.Request) (bool, error) {
	token, err := newToken()
	if err != nil {
		return false, err
	}
	ch := make(chan bool, 1)
	a.s.pending.Store(token, ch)
	defer a.s.pending.Delete(token)

	text := fmt.Sprintf("tomo wants to run *%s* [%s]\n%s", req.Tool, req.Class, req.Reason)
	body := map[string]any{
		"channel": a.channelID,
		"text":    text,
		"blocks": []any{
			map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
			map[string]any{"type": "actions", "elements": []any{
				map[string]any{"type": "button", "text": map[string]any{"type": "plain_text", "text": "Allow"}, "style": "primary", "value": token + "|1", "action_id": "allow"},
				map[string]any{"type": "button", "text": map[string]any{"type": "plain_text", "text": "Deny"}, "style": "danger", "value": token + "|0", "action_id": "deny"},
			}},
		},
	}
	if err := a.s.post(a.ctx, body); err != nil {
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

type envelope struct {
	Type       string          `json:"type"`
	EnvelopeID string          `json:"envelope_id"`
	Payload    json.RawMessage `json:"payload"`
}

func (s *Slack) dial(ctx context.Context, url string) (*websocket.Conn, error) {
	if s.Dial != nil {
		return s.Dial(ctx, url)
	}
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(1 << 20)
	return conn, nil
}

// openConnection asks Slack for a socket URL with the app-level token.
func (s *Slack) openConnection(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiBase()+"/apps.connections.open", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.AppToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("slack apps.connections.open: %s", out.Error)
	}
	return out.URL, nil
}

func (s *Slack) post(ctx context.Context, body map[string]any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiBase()+"/chat.postMessage", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+s.BotToken)
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("slack chat.postMessage: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if !out.OK {
		return fmt.Errorf("slack chat.postMessage: %s", out.Error)
	}
	return nil
}

func parseValue(data string) (token string, allow bool, ok bool) {
	tok, flag, found := strings.Cut(data, "|")
	if !found {
		return "", false, false
	}
	return tok, flag == "1", true
}

func newToken() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// splitMessage breaks text into parts no longer than max, preferring a newline
// so a message does not tear mid-line.
func splitMessage(text string, max int) []string {
	var parts []string
	for len(text) > max {
		cut := strings.LastIndex(text[:max], "\n")
		if cut <= 0 {
			cut = max
		}
		parts = append(parts, text[:cut])
		text = strings.TrimPrefix(text[cut:], "\n")
	}
	return append(parts, text)
}

func escapeItalic(s string) string {
	return strings.ReplaceAll(s, "_", " ")
}
