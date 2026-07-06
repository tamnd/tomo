// Package voice turns inbound speech into text. tomo does the transcription
// itself by shelling out to whisper.cpp, so no audio leaves the machine and
// there is no cloud speech API to key. A voice note becomes ordinary text in
// the session, which every downstream part already understands.
package voice

import (
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
