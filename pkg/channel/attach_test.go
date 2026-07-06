package channel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fileReply is a reply that can also receive files, for the send_file tests.
type fileReply struct {
	captureReply
	files []Attachment
}

func (f *fileReply) File(a Attachment) { f.files = append(f.files, a) }

func runAttach(t *testing.T, reply Reply, path, caption string) (string, error) {
	t.Helper()
	tool := attachTool(reply)
	in, err := json.Marshal(map[string]string{"path": path, "caption": caption})
	if err != nil {
		t.Fatal(err)
	}
	return tool.Run(context.Background(), in)
}

func TestAttachSendsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nfake"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := &fileReply{}
	out, err := runAttach(t, rep, path, "here is the chart")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.files) != 1 {
		t.Fatalf("expected one file sent, got %d", len(rep.files))
	}
	a := rep.files[0]
	if a.Name != "chart.png" || a.Mime != "image/png" || a.Caption != "here is the chart" {
		t.Errorf("attachment = %+v", a)
	}
	if !strings.Contains(out, "sent chart.png") {
		t.Errorf("result = %q", out)
	}
}

func TestAttachDegradesWhenChannelCannotCarryFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	// captureReply does not implement FileReply.
	out, err := runAttach(t, &captureReply{}, path, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "cannot carry files") || !strings.Contains(out, path) {
		t.Errorf("expected a fallback naming the path, got %q", out)
	}
}

func TestAttachRejectsMissingAndDir(t *testing.T) {
	if _, err := runAttach(t, &fileReply{}, filepath.Join(t.TempDir(), "nope.png"), ""); err == nil {
		t.Error("expected an error for a missing file")
	}
	if _, err := runAttach(t, &fileReply{}, t.TempDir(), ""); err == nil {
		t.Error("expected an error for a directory")
	}
}

func TestMimeOf(t *testing.T) {
	cases := map[string]string{
		"a.png":  "image/png",
		"a.jpeg": "image/jpeg",
		"a.pdf":  "application/pdf",
		"a.md":   "text/plain",
	}
	for name, want := range cases {
		if got := mimeOf(name, nil); got != want {
			t.Errorf("mimeOf(%q) = %q, want %q", name, got, want)
		}
	}
}
