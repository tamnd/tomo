// Package webchat is tomo's built-in channel: a small single-page UI served
// on localhost and a streaming endpoint behind it. No third-party dependency,
// so it is the channel that always works and the one to develop against.
package webchat

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/tamnd/tomo/pkg/channel"
	"github.com/tamnd/tomo/pkg/policy"
)

//go:embed index.html
var assets embed.FS

// WebChat serves the UI and turns browser requests into exchanges.
type WebChat struct {
	Addr string // listen address, e.g. 127.0.0.1:8765

	handler channel.Handler
	pending sync.Map // token -> chan bool
}

// Name implements channel.Channel.
func (w *WebChat) Name() string { return "web" }

// Caps implements channel.Channel. The browser streams text, renders approval
// buttons, and can attach images pasted or picked in the page.
func (w *WebChat) Caps() channel.Caps {
	return channel.Caps{Stream: true, Buttons: true, Media: true}
}

// Run starts the HTTP server and blocks until ctx is cancelled.
func (w *WebChat) Run(ctx context.Context, h channel.Handler) error {
	w.handler = h

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", w.serveIndex)
	mux.HandleFunc("POST /api/chat", w.serveChat)
	mux.HandleFunc("POST /api/approve", w.serveApprove)

	srv := &http.Server{Addr: w.Addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		ln, err := net.Listen("tcp", w.Addr)
		if err != nil {
			errc <- err
			return
		}
		errc <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (w *WebChat) serveIndex(rw http.ResponseWriter, r *http.Request) {
	page, err := assets.ReadFile("index.html")
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = rw.Write(page)
}

func (w *WebChat) serveChat(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Session string   `json:"session"`
		Text    string   `json:"text"`
		Images  []string `json:"images"` // data: URLs of pasted or attached images
		Audio   []string `json:"audio"`  // data: URLs of recorded voice notes
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Session == "" {
		body.Session = "web"
	}
	in := channel.Inbound{Chat: body.Session, User: "local", Text: body.Text}
	for _, u := range body.Images {
		// A bad image is dropped, not fatal; the text turn still runs.
		if img, err := channel.DecodeDataURL(u); err == nil {
			in.Images = append(in.Images, img)
		}
	}
	for _, u := range body.Audio {
		if clip, err := channel.DecodeAudioDataURL(u); err == nil {
			in.Audio = append(in.Audio, clip)
		}
	}
	flusher, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("Connection", "keep-alive")

	sse := &sseReply{rw: rw, flusher: flusher}
	x := channel.Exchange{
		In:       in,
		Reply:    sse,
		Approver: &webApprover{wc: w, sse: sse, ctx: r.Context()},
	}
	w.handler(r.Context(), x)
}

func (w *WebChat) serveApprove(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
		Allow bool   `json:"allow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	if ch, ok := w.pending.LoadAndDelete(body.Token); ok {
		ch.(chan bool) <- body.Allow
	}
	rw.WriteHeader(http.StatusNoContent)
}

// sseReply writes channel events to the browser as server-sent events. All
// writes come from the single turn goroutine, so no locking is needed.
type sseReply struct {
	rw      http.ResponseWriter
	flusher http.Flusher
}

func (s *sseReply) event(kind string, data map[string]any) {
	data["type"] = kind
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(s.rw, "data: %s\n\n", payload)
	s.flusher.Flush()
}

func (s *sseReply) Chunk(text string)  { s.event("chunk", map[string]any{"text": text}) }
func (s *sseReply) Notice(text string) { s.event("notice", map[string]any{"text": text}) }
func (s *sseReply) Done()              { s.event("done", map[string]any{}) }

// Voice sends a spoken reply as an audio event; the page plays it inline.
func (s *sseReply) Voice(clip channel.Clip) {
	s.event("audio", map[string]any{
		"mime": mimeForExt(clip.Ext),
		"data": base64.StdEncoding.EncodeToString(clip.Data),
	})
}

// File sends a produced file as a file event; the page shows an image inline
// and anything else as a download link.
func (s *sseReply) File(a channel.Attachment) {
	s.event("file", map[string]any{
		"name":    a.Name,
		"mime":    a.Mime,
		"caption": a.Caption,
		"data":    base64.StdEncoding.EncodeToString(a.Data),
	})
}

// mimeForExt maps a clip extension to the media type the browser needs on the
// data URL. Voice notes are opus in an ogg container by default.
func mimeForExt(ext string) string {
	switch ext {
	case ".wav":
		return "audio/wav"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	default:
		return "audio/ogg"
	}
}

// webApprover asks the browser to approve a call by emitting an approval
// event, then blocks until /api/approve resolves the token or the request ends.
type webApprover struct {
	wc  *WebChat
	sse *sseReply
	ctx context.Context
}

func (a *webApprover) Approve(_ context.Context, req policy.Request) (bool, error) {
	token, err := newToken()
	if err != nil {
		return false, err
	}
	ch := make(chan bool, 1)
	a.wc.pending.Store(token, ch)
	defer a.wc.pending.Delete(token)

	a.sse.event("approval", map[string]any{
		"token":  token,
		"tool":   req.Tool,
		"class":  string(req.Class),
		"input":  string(req.Input),
		"reason": req.Reason,
	})

	select {
	case allow := <-ch:
		return allow, nil
	case <-a.ctx.Done():
		return false, a.ctx.Err()
	}
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
