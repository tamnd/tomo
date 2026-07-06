// Package discord is tomo's Discord channel. It holds a gateway websocket open
// for inbound messages and button clicks, and sends over the REST API. Only
// allowlisted channels are served, so adding the bot to a server does not hand
// every channel an agent. Approvals render as Allow/Deny buttons.
package discord

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

// Gateway opcodes, the slice we use.
const (
	opDispatch  = 0
	opHeartbeat = 1
	opIdentify  = 2
	opReconnect = 7
	opInvalid   = 9
	opHello     = 10
	opHeartACK  = 11
)

// Intents. MESSAGE_CONTENT is privileged and must be enabled in the bot's
// settings, or message text arrives empty.
const intents = 1<<0 | 1<<9 | 1<<12 | 1<<15

// Discord serves an allowlisted set of channels over one gateway connection.
type Discord struct {
	Token      string
	Allow      []string // channel ids permitted to talk to the bot
	BaseURL    string   // REST base, default https://discord.com/api/v10
	GatewayURL string   // default wss://gateway.discord.gg/?v=10&encoding=json
	Client     *http.Client

	// Dial connects the gateway. Nil uses websocket.Dial; tests override it.
	Dial func(ctx context.Context, url string) (*websocket.Conn, error)

	handler channel.Handler
	pending sync.Map // token -> chan bool
}

// Name implements channel.Channel.
func (d *Discord) Name() string { return "discord" }

// Caps implements channel.Channel. Discord carries media and renders buttons.
func (d *Discord) Caps() channel.Caps { return channel.Caps{Media: true, Buttons: true} }

func (d *Discord) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (d *Discord) restBase() string {
	if d.BaseURL != "" {
		return strings.TrimSuffix(d.BaseURL, "/")
	}
	return "https://discord.com/api/v10"
}

func (d *Discord) gatewayURL() string {
	if d.GatewayURL != "" {
		return d.GatewayURL
	}
	return "wss://gateway.discord.gg/?v=10&encoding=json"
}

func (d *Discord) allowed(channelID string) bool {
	return slices.Contains(d.Allow, channelID)
}

// Post pushes a message to a channel outside a reply, for scheduled runs. It
// implements schedule.Poster.
func (d *Discord) Post(ctx context.Context, chat, text string) error {
	for _, part := range splitMessage(text, 2000) {
		if err := d.sendMessage(ctx, chat, map[string]any{"content": part}); err != nil {
			return err
		}
	}
	return nil
}

// Run holds the gateway connection open until ctx is cancelled, reconnecting
// with a short backoff when it drops.
func (d *Discord) Run(ctx context.Context, h channel.Handler) error {
	d.handler = h
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := d.session(ctx); err != nil && ctx.Err() == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
		}
	}
}

// session runs one gateway connection: read Hello, heartbeat, identify, then
// dispatch until the socket closes.
func (d *Discord) session(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, err := d.dial(ctx, d.gatewayURL())
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	var hello struct {
		HeartbeatInterval int64 `json:"heartbeat_interval"`
	}
	first, err := readPayload(ctx, conn)
	if err != nil {
		return err
	}
	if first.Op != opHello {
		return fmt.Errorf("discord: first frame op %d, want hello", first.Op)
	}
	if err := json.Unmarshal(first.D, &hello); err != nil {
		return err
	}

	sess := &gwSession{conn: conn}
	go sess.heartbeatLoop(ctx, time.Duration(hello.HeartbeatInterval)*time.Millisecond)

	if err := sess.write(ctx, opIdentify, map[string]any{
		"token":   d.Token,
		"intents": intents,
		"properties": map[string]string{
			"os": "linux", "browser": "tomo", "device": "tomo",
		},
	}); err != nil {
		return err
	}

	for {
		p, err := readPayload(ctx, conn)
		if err != nil {
			return err
		}
		sess.setSeq(p.S)
		switch p.Op {
		case opHeartbeat:
			if err := sess.write(ctx, opHeartbeat, sess.seqValue()); err != nil {
				return err
			}
		case opReconnect, opInvalid:
			return fmt.Errorf("discord: gateway asked to reconnect (op %d)", p.Op)
		case opHeartACK:
			// nothing to do
		case opDispatch:
			d.dispatch(ctx, p.T, p.D)
		}
	}
}

func (d *Discord) dispatch(ctx context.Context, event string, data json.RawMessage) {
	switch event {
	case "MESSAGE_CREATE":
		d.onMessage(ctx, data)
	case "INTERACTION_CREATE":
		d.onInteraction(ctx, data)
	}
}

func (d *Discord) onMessage(ctx context.Context, data json.RawMessage) {
	var m messageCreate
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	// Ignore our own and other bots' messages, and messages with nothing in
	// them. A bare image with no text is still worth handling.
	if m.Author.Bot || !d.allowed(m.ChannelID) || (m.Content == "" && len(m.Attachments) == 0) {
		return
	}
	in := channel.Inbound{Chat: m.ChannelID, User: m.Author.ID, Text: m.Content}
	for _, att := range m.Attachments {
		if !strings.HasPrefix(att.ContentType, "image/") {
			continue
		}
		// Discord attachment URLs are public CDN links, no auth needed.
		if img, err := channel.FetchImage(ctx, http.DefaultClient, att.URL, nil); err == nil {
			in.Images = append(in.Images, img)
		}
	}
	reply := &dcReply{d: d, ctx: ctx, channelID: m.ChannelID}
	x := channel.Exchange{
		In:       in,
		Reply:    reply,
		Approver: &dcApprover{d: d, ctx: ctx, channelID: m.ChannelID},
	}
	// Do not block the read loop; the router serializes per channel.
	go d.handler(ctx, x)
}

func (d *Discord) onInteraction(ctx context.Context, data json.RawMessage) {
	var it interaction
	if err := json.Unmarshal(data, &it); err != nil {
		return
	}
	if it.Type != interactionComponent {
		return
	}
	token, allow, ok := parseCustom(it.Data.CustomID)
	if ok {
		if ch, loaded := d.pending.LoadAndDelete(token); loaded {
			ch.(chan bool) <- allow
		}
	}
	// Acknowledge so the client stops showing a spinner. Type 6 keeps the
	// original message as is (deferred update).
	_ = d.callback(ctx, it.ID, it.Token, 6)
}

// gwSession carries the per-connection write lock and last sequence number.
type gwSession struct {
	conn *websocket.Conn

	mu  sync.Mutex
	seq *int64
}

func (s *gwSession) setSeq(seq *int64) {
	if seq == nil {
		return
	}
	s.mu.Lock()
	s.seq = seq
	s.mu.Unlock()
}

func (s *gwSession) seqValue() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seq == nil {
		return nil
	}
	return *s.seq
}

// write serializes gateway writes; coder/websocket allows only one writer at a
// time and the heartbeat loop runs alongside the reader.
func (s *gwSession) write(ctx context.Context, op int, d any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return wsjson.Write(ctx, s.conn, map[string]any{"op": op, "d": d})
}

func (s *gwSession) heartbeatLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 41250 * time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.write(ctx, opHeartbeat, s.seqValue()); err != nil {
				return
			}
		}
	}
}

// dcReply buffers streamed text and sends it once the turn is done, splitting
// at Discord's 2000-character limit. Notices go out immediately.
type dcReply struct {
	d         *Discord
	ctx       context.Context
	channelID string
	buf       strings.Builder
}

func (r *dcReply) Chunk(text string) { r.buf.WriteString(text) }

func (r *dcReply) Notice(text string) {
	_ = r.d.sendMessage(r.ctx, r.channelID, map[string]any{"content": "*" + escapeItalic(text) + "*"})
}

func (r *dcReply) Done() {
	text := strings.TrimSpace(r.buf.String())
	if text == "" {
		return
	}
	for _, part := range splitMessage(text, 2000) {
		_ = r.d.sendMessage(r.ctx, r.channelID, map[string]any{"content": part})
	}
}

// dcApprover posts Allow/Deny buttons and waits for the click.
type dcApprover struct {
	d         *Discord
	ctx       context.Context
	channelID string
}

func (a *dcApprover) Approve(_ context.Context, req policy.Request) (bool, error) {
	token, err := newToken()
	if err != nil {
		return false, err
	}
	ch := make(chan bool, 1)
	a.d.pending.Store(token, ch)
	defer a.d.pending.Delete(token)

	text := fmt.Sprintf("tomo wants to run **%s** [%s]\n%s", req.Tool, req.Class, req.Reason)
	body := map[string]any{
		"content": text,
		"components": []any{map[string]any{
			"type": 1,
			"components": []any{
				map[string]any{"type": 2, "style": 3, "label": "Allow", "custom_id": token + "|1"},
				map[string]any{"type": 2, "style": 4, "label": "Deny", "custom_id": token + "|0"},
			},
		}},
	}
	if err := a.d.sendMessage(a.ctx, a.channelID, body); err != nil {
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

// Wire types for the events we read.

type messageCreate struct {
	ID          string       `json:"id"`
	ChannelID   string       `json:"channel_id"`
	Content     string       `json:"content"`
	GuildID     string       `json:"guild_id"`
	Attachments []attachment `json:"attachments"`
	Author      struct {
		ID  string `json:"id"`
		Bot bool   `json:"bot"`
	} `json:"author"`
}

// attachment is one file on a message. content_type is set for uploads, so we
// can tell images from everything else without downloading first.
type attachment struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
}

const interactionComponent = 3

type interaction struct {
	ID        string `json:"id"`
	Token     string `json:"token"`
	Type      int    `json:"type"`
	ChannelID string `json:"channel_id"`
	Data      struct {
		CustomID string `json:"custom_id"`
	} `json:"data"`
}

type gwPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s"`
	T  string          `json:"t"`
}

func readPayload(ctx context.Context, conn *websocket.Conn) (gwPayload, error) {
	var p gwPayload
	err := wsjson.Read(ctx, conn, &p)
	return p, err
}

func (d *Discord) dial(ctx context.Context, url string) (*websocket.Conn, error) {
	if d.Dial != nil {
		return d.Dial(ctx, url)
	}
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	// Gateway frames can be large; lift the default read cap.
	conn.SetReadLimit(1 << 20)
	return conn, nil
}

func (d *Discord) sendMessage(ctx context.Context, channelID string, body map[string]any) error {
	return d.rest(ctx, http.MethodPost, "/channels/"+channelID+"/messages", body)
}

func (d *Discord) callback(ctx context.Context, id, token string, kind int) error {
	return d.rest(ctx, http.MethodPost, "/interactions/"+id+"/"+token+"/callback", map[string]any{"type": kind})
}

func (d *Discord) rest(ctx context.Context, method, path string, body map[string]any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, d.restBase()+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+d.Token)
	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return fmt.Errorf("discord %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func parseCustom(data string) (token string, allow bool, ok bool) {
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
	return strings.ReplaceAll(s, "*", " ")
}
