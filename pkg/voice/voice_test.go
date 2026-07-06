package voice

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormExt(t *testing.T) {
	cases := map[string]string{
		"":     ".ogg",
		"ogg":  ".ogg",
		".OGG": ".ogg",
		".m4a": ".m4a",
		" wav": ".wav",
	}
	for in, want := range cases {
		if got := normExt(in); got != want {
			t.Errorf("normExt(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTranscribeNoModel(t *testing.T) {
	w := &Whisper{}
	if _, err := w.Transcribe(context.Background(), []byte("x"), ".ogg"); err == nil {
		t.Fatal("expected an error with no model configured")
	}
}

func TestTranscribeEmptyAudio(t *testing.T) {
	w := &Whisper{Model: "m.bin"}
	if _, err := w.Transcribe(context.Background(), nil, ".ogg"); err == nil {
		t.Fatal("expected an error on empty audio")
	}
}

func TestTranscribeConvertsThenReads(t *testing.T) {
	var sawFFmpeg, sawWhisper bool
	w := &Whisper{
		Model: "model.bin",
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch {
			case strings.Contains(name, "ffmpeg"):
				sawFFmpeg = true
				// ffmpeg writes the wav named as its last arg.
				return nil, os.WriteFile(args[len(args)-1], []byte("wavdata"), 0o600)
			default:
				sawWhisper = true
				// whisper writes <base>.txt; -of gives the base path.
				base := ofArg(args)
				return nil, os.WriteFile(base+".txt", []byte("  hello there  \n"), 0o600)
			}
		},
	}

	got, err := w.Transcribe(context.Background(), []byte("oggbytes"), ".ogg")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello there" {
		t.Errorf("transcript = %q, want trimmed 'hello there'", got)
	}
	if !sawFFmpeg {
		t.Error("a non-wav clip should be decoded with ffmpeg")
	}
	if !sawWhisper {
		t.Error("whisper was not run")
	}
}

func TestTranscribeWavSkipsFFmpeg(t *testing.T) {
	var sawFFmpeg bool
	w := &Whisper{
		Model: "model.bin",
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if strings.Contains(name, "ffmpeg") {
				sawFFmpeg = true
			}
			return nil, os.WriteFile(ofArg(args)+".txt", []byte("wav words"), 0o600)
		},
	}
	got, err := w.Transcribe(context.Background(), []byte("RIFFdata"), ".wav")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wav words" {
		t.Errorf("transcript = %q", got)
	}
	if sawFFmpeg {
		t.Error("a wav clip should not be re-decoded")
	}
}

func TestSynthesizeNoModel(t *testing.T) {
	s := &Speaker{}
	if _, _, err := s.Synthesize(context.Background(), "hello"); err == nil {
		t.Fatal("expected an error with no tts model configured")
	}
}

func TestSynthesizeEmptyText(t *testing.T) {
	s := &Speaker{Model: "voice.onnx"}
	if _, _, err := s.Synthesize(context.Background(), "   "); err == nil {
		t.Fatal("expected an error on empty text")
	}
}

func TestSynthesizeRendersThenEncodes(t *testing.T) {
	var sawPiper, sawFFmpeg bool
	var spoken string
	s := &Speaker{
		Model: "voice.onnx",
		run: func(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
			switch {
			case strings.Contains(name, "piper"):
				sawPiper = true
				spoken = string(stdin)
				// piper writes the wav named after -f.
				return nil, os.WriteFile(fArg(args), []byte("wavdata"), 0o600)
			default:
				sawFFmpeg = true
				// ffmpeg writes the ogg named as its last arg.
				return nil, os.WriteFile(args[len(args)-1], []byte("oggdata"), 0o600)
			}
		},
	}

	audio, ext, err := s.Synthesize(context.Background(), "read this")
	if err != nil {
		t.Fatal(err)
	}
	if string(audio) != "oggdata" || ext != ".ogg" {
		t.Errorf("audio = %q ext = %q, want the encoded ogg", audio, ext)
	}
	if spoken != "read this" {
		t.Errorf("piper stdin = %q, want the text to speak", spoken)
	}
	if !sawPiper || !sawFFmpeg {
		t.Errorf("both piper and ffmpeg should run, piper=%v ffmpeg=%v", sawPiper, sawFFmpeg)
	}
}

// fArg returns the value following -f in a piper argument list.
func fArg(args []string) string {
	for i, a := range args {
		if a == "-f" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return filepath.Join(os.TempDir(), "speech.wav")
}

// ofArg returns the value following -of in a whisper argument list.
func ofArg(args []string) string {
	for i, a := range args {
		if a == "-of" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return filepath.Join(os.TempDir(), "out")
}
