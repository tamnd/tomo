// Package telegram is tomo's Telegram channel. It long-polls getUpdates, so
// there is no webhook to expose, and it answers approvals with inline buttons.
// Only allowlisted chats are served; an unknown chat is ignored, not replied
// to, so the bot token leaking does not hand anyone an agent.
package telegram

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/tomo/pkg/channel"
	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/provider"
)

// Telegram serves an allowlisted set of chats through the Bot API.
type Telegram struct {
	Token   string
	Allow   []int64 // chat ids permitted to talk to the bot
	BaseURL string  // default https://api.telegram.org, overridable for tests
	Client  *http.Client

	handler channel.Handler
	pending sync.Map // token -> chan bool
}

// Name implements channel.Channel.
func (t *Telegram) Name() string { return "telegram" }

// Caps implements channel.Channel. Text is sent whole (no live editing yet),
// and approvals render as inline buttons.
func (t *Telegram) Caps() channel.Caps { return channel.Caps{Media: true, Buttons: true} }

func (t *Telegram) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return &http.Client{Timeout: 65 * time.Second}
}

func (t *Telegram) baseURL() string {
	if t.BaseURL != "" {
		return strings.TrimSuffix(t.BaseURL, "/")
	}
	return "https://api.telegram.org"
}

func (t *Telegram) allowed(chatID int64) bool {
	return slices.Contains(t.Allow, chatID)
}

// Post pushes a message to a chat outside a reply, for scheduled runs. It
// implements schedule.Poster.
func (t *Telegram) Post(ctx context.Context, chat, text string) error {
	id, err := strconv.ParseInt(chat, 10, 64)
	if err != nil {
		return err
	}
	for _, part := range splitMessage(text, 4096) {
		if err := t.send(ctx, id, part, false); err != nil {
			return err
		}
	}
	return nil
}

// Run long-polls until ctx is cancelled.
func (t *Telegram) Run(ctx context.Context, h channel.Handler) error {
	t.handler = h
	var offset int64
	for {
		if ctx.Err() != nil {
			return nil
		}
		updates, err := t.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// Back off on transient errors rather than hammering the API.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			t.dispatch(ctx, u)
		}
	}
}

func (t *Telegram) dispatch(ctx context.Context, u update) {
	switch {
	case u.CallbackQuery != nil:
		t.resolveCallback(ctx, u.CallbackQuery)
	case u.Message != nil && u.Message.hasContent():
		m := u.Message
		chatID := m.Chat.ID
		if !t.allowed(chatID) {
			return
		}
		text := m.Text
		if text == "" {
			text = m.Caption
		}
		in := channel.Inbound{
			Chat: strconv.FormatInt(chatID, 10),
			User: strconv.FormatInt(m.From.ID, 10),
			Text: text,
		}
		if len(m.Photo) > 0 {
			// The last size is the largest; send that to the model.
			if img, err := t.fetchPhoto(ctx, m.Photo[len(m.Photo)-1].FileID); err == nil {
				in.Images = append(in.Images, img)
			}
		}
		if ref := m.audio(); ref != nil {
			if clip, err := t.fetchAudio(ctx, ref.FileID, ref.MimeType); err == nil {
				in.Audio = append(in.Audio, clip)
			}
		}
		reply := &tgReply{tg: t, ctx: ctx, chatID: chatID}
		x := channel.Exchange{
			In:       in,
			Reply:    reply,
			Approver: &tgApprover{tg: t, ctx: ctx, chatID: chatID},
		}
		// Run each conversation's turn without blocking the poll loop; the
		// router serializes per chat.
		go t.handler(ctx, x)
	}
}

// fetchPhoto resolves a Telegram file id to a downloadable URL and pulls the
// image down as a model-ready block.
func (t *Telegram) fetchPhoto(ctx context.Context, fileID string) (provider.Block, error) {
	path, err := t.filePath(ctx, fileID)
	if err != nil {
		return provider.Block{}, err
	}
	url := t.baseURL() + "/file/bot" + t.Token + "/" + path
	return channel.FetchImage(ctx, t.client(), url, nil)
}

// fetchAudio resolves a voice or audio file id and pulls the clip down for
// transcription. Telegram's file path carries a known extension (.oga for a
// voice note), which is enough for the transcriber to decode it.
func (t *Telegram) fetchAudio(ctx context.Context, fileID, _ string) (channel.Clip, error) {
	path, err := t.filePath(ctx, fileID)
	if err != nil {
		return channel.Clip{}, err
	}
	url := t.baseURL() + "/file/bot" + t.Token + "/" + path
	return channel.FetchAudio(ctx, t.client(), url, nil)
}

// filePath calls getFile to turn a file id into the server-side path the file
// endpoint serves.
func (t *Telegram) filePath(ctx context.Context, fileID string) (string, error) {
	v := url.Values{}
	v.Set("file_id", fileID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.method("getFile")+"?"+v.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := t.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if !out.OK || out.Result.FilePath == "" {
		return "", fmt.Errorf("telegram getFile: no path for %s", fileID)
	}
	return out.Result.FilePath, nil
}

func (t *Telegram) resolveCallback(ctx context.Context, cq *callbackQuery) {
	token, allow, ok := parseCallback(cq.Data)
	if ok {
		if ch, loaded := t.pending.LoadAndDelete(token); loaded {
			ch.(chan bool) <- allow
		}
	}
	_ = t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": cq.ID})
}

// tgReply buffers streamed text and sends it as one message on Done, and sends
// notices immediately as their own lines.
type tgReply struct {
	tg     *Telegram
	ctx    context.Context
	chatID int64
	buf    strings.Builder
}

func (r *tgReply) Chunk(text string) { r.buf.WriteString(text) }

func (r *tgReply) Notice(text string) {
	_ = r.tg.send(r.ctx, r.chatID, "_"+escapeItalic(text)+"_", true)
}

func (r *tgReply) Done() {
	text := strings.TrimSpace(r.buf.String())
	if text == "" {
		return
	}
	for _, part := range splitMessage(text, 4096) {
		_ = r.tg.send(r.ctx, r.chatID, part, false)
	}
}

// Voice sends a spoken reply as a Telegram voice note.
func (r *tgReply) Voice(clip channel.Clip) {
	_ = r.tg.sendVoice(r.ctx, r.chatID, clip.Data, clip.Ext)
}

// File sends a produced file: an image as a photo, anything else as a document.
func (r *tgReply) File(a channel.Attachment) {
	_ = r.tg.sendFile(r.ctx, r.chatID, a)
}

// tgApprover asks with an inline keyboard and waits for the button press.
type tgApprover struct {
	tg     *Telegram
	ctx    context.Context
	chatID int64
}

func (a *tgApprover) Approve(_ context.Context, req policy.Request) (bool, error) {
	token, err := newToken()
	if err != nil {
		return false, err
	}
	ch := make(chan bool, 1)
	a.tg.pending.Store(token, ch)
	defer a.tg.pending.Delete(token)

	text := fmt.Sprintf("tomo wants to run *%s* [%s]\n%s", req.Tool, req.Class, req.Reason)
	kb := map[string]any{
		"inline_keyboard": [][]map[string]any{{
			{"text": "Allow", "callback_data": token + "|1"},
			{"text": "Deny", "callback_data": token + "|0"},
		}},
	}
	if err := a.tg.call(a.ctx, "sendMessage", map[string]any{
		"chat_id": a.chatID, "text": text, "parse_mode": "Markdown", "reply_markup": kb,
	}); err != nil {
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

// Wire types for the slice of the Bot API we touch.

type update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *message       `json:"message"`
	CallbackQuery *callbackQuery `json:"callback_query"`
}

type message struct {
	Text    string      `json:"text"`
	Caption string      `json:"caption"`
	Photo   []photoSize `json:"photo"`
	Voice   *audioFile  `json:"voice"`
	Audio   *audioFile  `json:"audio"`
	Chat    struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
}

// hasContent reports whether a message carries anything worth a turn.
func (m *message) hasContent() bool {
	return m.Text != "" || len(m.Photo) > 0 || m.audio() != nil
}

// audio returns the voice note or audio file on the message, preferring a
// voice note, or nil if there is neither.
func (m *message) audio() *audioFile {
	if m.Voice != nil {
		return m.Voice
	}
	return m.Audio
}

// photoSize is one rendition of a photo. Telegram sends several; the last is
// the largest.
type photoSize struct {
	FileID string `json:"file_id"`
}

// audioFile is a voice note or music file. mime_type tells us the container.
type audioFile struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type"`
}

type callbackQuery struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}

func (t *Telegram) getUpdates(ctx context.Context, offset int64) ([]update, error) {
	v := url.Values{}
	v.Set("timeout", "50")
	v.Set("offset", strconv.FormatInt(offset, 10))
	v.Set("allowed_updates", `["message","callback_query"]`)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.method("getUpdates")+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := t.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		OK          bool     `json:"ok"`
		Description string   `json:"description"`
		Result      []update `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram getUpdates: %s", out.Description)
	}
	return out.Result, nil
}

func (t *Telegram) send(ctx context.Context, chatID int64, text string, markdown bool) error {
	body := map[string]any{"chat_id": chatID, "text": text}
	if markdown {
		body["parse_mode"] = "Markdown"
	}
	return t.call(ctx, "sendMessage", body)
}

func (t *Telegram) call(ctx context.Context, method string, body map[string]any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.method(method), bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return fmt.Errorf("telegram %s: %s: %s", method, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// sendVoice uploads an audio clip. An opus/ogg clip goes as a real voice note;
// anything else falls back to sendAudio so the user still gets a playable file.
func (t *Telegram) sendVoice(ctx context.Context, chatID int64, data []byte, ext string) error {
	method, field := "sendVoice", "voice"
	if ext != ".ogg" {
		method, field = "sendAudio", "audio"
	}
	return t.upload(ctx, method, field, "reply"+ext, data, map[string]string{
		"chat_id": strconv.FormatInt(chatID, 10),
	})
}

// sendFile uploads a produced file. Images go as a photo so they render inline;
// everything else goes as a document.
func (t *Telegram) sendFile(ctx context.Context, chatID int64, a channel.Attachment) error {
	method, field := "sendDocument", "document"
	if strings.HasPrefix(a.Mime, "image/") && a.Mime != "image/svg+xml" {
		method, field = "sendPhoto", "photo"
	}
	extra := map[string]string{"chat_id": strconv.FormatInt(chatID, 10)}
	if a.Caption != "" {
		extra["caption"] = a.Caption
	}
	return t.upload(ctx, method, field, a.Name, a.Data, extra)
}

// upload posts a multipart form with one file field plus the given text fields.
func (t *Telegram) upload(ctx context.Context, method, field, filename string, data []byte, fields map[string]string) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return err
		}
	}
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.method(method), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := t.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return fmt.Errorf("telegram %s: %s: %s", method, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func (t *Telegram) method(name string) string {
	return t.baseURL() + "/bot" + t.Token + "/" + name
}

func parseCallback(data string) (token string, allow bool, ok bool) {
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

// splitMessage breaks text into parts no longer than max, preferring to split
// on a newline so a message does not tear mid-line.
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
