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

// ofArg returns the value following -of in a whisper argument list.
func ofArg(args []string) string {
	for i, a := range args {
		if a == "-of" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return filepath.Join(os.TempDir(), "out")
}
