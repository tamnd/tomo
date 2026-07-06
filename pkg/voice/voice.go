// Package voice carries speech both ways. Inbound, it transcribes voice notes
// with whisper.cpp; outbound, it renders replies with piper. Both run locally
// by shelling out, so no audio leaves the machine and there is no cloud speech
// API to key. A voice note becomes ordinary text in the session, and a spoken
// reply becomes an Opus clip a channel can send back.
package voice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Transcriber turns a speech clip into text. ext is the source container
// (".ogg", ".m4a", and so on) so the implementation can decode it.
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, ext string) (string, error)
}

// Synthesizer turns reply text into a speech clip. ext is the container of the
// returned bytes so a channel can label the upload.
type Synthesizer interface {
	Synthesize(ctx context.Context, text string) (audio []byte, ext string, err error)
}

// Speaker synthesizes with piper and packs the result as an Opus voice note.
// It runs piper to render a WAV, then encodes that to OGG/Opus with ffmpeg,
// which is the container messaging apps treat as a real voice note. Both steps
// are local, so nothing is sent to a cloud voice API.
type Speaker struct {
	Bin    string // piper cli, default "piper"
	Model  string // path to a piper voice model, required
	FFmpeg string // encoder to opus, default "ffmpeg"

	// run executes a command, feeding stdin when given, and returns its
	// combined output. Nil uses os/exec; tests override it.
	run func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)
}

func (s *Speaker) tts() string {
	if s.Bin != "" {
		return s.Bin
	}
	return "piper"
}

func (s *Speaker) ffmpeg() string {
	if s.FFmpeg != "" {
		return s.FFmpeg
	}
	return "ffmpeg"
}

func (s *Speaker) exec(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	if s.run != nil {
		return s.run(ctx, stdin, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return cmd.CombinedOutput()
}

// Synthesize renders text to speech in a scratch dir and returns the Opus
// bytes. The scratch dir is always cleaned up.
func (s *Speaker) Synthesize(ctx context.Context, text string) ([]byte, string, error) {
	if s.Model == "" {
		return nil, "", errors.New("voice: no tts model configured")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, "", errors.New("voice: nothing to speak")
	}

	dir, err := os.MkdirTemp("", "tomo-speak")
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	wav := filepath.Join(dir, "speech.wav")
	if out, err := s.exec(ctx, []byte(text), s.tts(), "-m", s.Model, "-f", wav); err != nil {
		return nil, "", fmt.Errorf("voice: piper: %w: %s", err, trim(out))
	}

	ogg := filepath.Join(dir, "speech.ogg")
	if out, err := s.exec(ctx, nil, s.ffmpeg(), "-nostdin", "-i", wav, "-c:a", "libopus", "-b:a", "24k", "-y", ogg); err != nil {
		return nil, "", fmt.Errorf("voice: encode opus: %w: %s", err, trim(out))
	}

	audio, err := os.ReadFile(ogg)
	if err != nil {
		return nil, "", fmt.Errorf("voice: read speech: %w", err)
	}
	return audio, ".ogg", nil
}

// Whisper transcribes with whisper.cpp. It writes the clip to a temp file,
// converts it to 16 kHz mono WAV with ffmpeg when the source is not already
// WAV (voice notes never are), then runs the whisper binary and reads back the
// transcript it writes as a sidecar text file.
type Whisper struct {
	Bin    string // whisper.cpp cli, default "whisper-cli"
	Model  string // path to a ggml model file, required
	FFmpeg string // decoder for non-wav input, default "ffmpeg"

	// run executes a command and returns its combined output. Nil uses
	// os/exec; tests override it.
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (w *Whisper) bin() string {
	if w.Bin != "" {
		return w.Bin
	}
	return "whisper-cli"
}

func (w *Whisper) ffmpeg() string {
	if w.FFmpeg != "" {
		return w.FFmpeg
	}
	return "ffmpeg"
}

func (w *Whisper) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if w.run != nil {
		return w.run(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Transcribe writes the clip to a scratch dir, decodes it to WAV if needed, and
// runs whisper over it. The scratch dir is always cleaned up.
func (w *Whisper) Transcribe(ctx context.Context, audio []byte, ext string) (string, error) {
	if w.Model == "" {
		return "", errors.New("voice: no whisper model configured")
	}
	if len(audio) == 0 {
		return "", errors.New("voice: empty audio")
	}

	dir, err := os.MkdirTemp("", "tomo-voice")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	src := filepath.Join(dir, "clip"+normExt(ext))
	if err := os.WriteFile(src, audio, 0o600); err != nil {
		return "", err
	}

	wav := src
	if !strings.EqualFold(normExt(ext), ".wav") {
		wav = filepath.Join(dir, "clip.wav")
		if out, err := w.exec(ctx, w.ffmpeg(), "-nostdin", "-i", src, "-ar", "16000", "-ac", "1", "-y", wav); err != nil {
			return "", fmt.Errorf("voice: decode audio: %w: %s", err, trim(out))
		}
	}

	base := filepath.Join(dir, "out")
	if out, err := w.exec(ctx, w.bin(), "-m", w.Model, "-f", wav, "-otxt", "-of", base, "-nt", "-np"); err != nil {
		return "", fmt.Errorf("voice: whisper: %w: %s", err, trim(out))
	}

	txt, err := os.ReadFile(base + ".txt")
	if err != nil {
		return "", fmt.Errorf("voice: read transcript: %w", err)
	}
	return strings.TrimSpace(string(txt)), nil
}

// normExt returns a clean, dotted, lowercase extension, defaulting to .ogg
// which is what most messaging voice notes use.
func normExt(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ".ogg"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return ext
}

func trim(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
