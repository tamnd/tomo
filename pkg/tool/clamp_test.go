package tool

import (
	"strings"
	"testing"
)

func TestClampShortPassesThrough(t *testing.T) {
	s := "line one\nline two\n"
	if got := Clamp(s, 1024, ""); got != s {
		t.Errorf("short input changed: %q", got)
	}
}

// A clamped result must keep the frame at the head and the verdict at the
// tail, the two ends a verify command puts its signal on.
func TestClampKeepsHeadAndTail(t *testing.T) {
	var b strings.Builder
	b.WriteString("$ go test ./...\n")
	for range 2000 {
		b.WriteString("some noisy middle line that repeats over and over again\n")
	}
	b.WriteString("FAIL: TestX assertion mismatch\n")
	s := b.String()

	got := Clamp(s, 8*1024, "")
	if len(got) > 9*1024 {
		t.Fatalf("clamped length = %d, want near 8KB", len(got))
	}
	if !strings.HasPrefix(got, "$ go test ./...\n") {
		t.Errorf("head dropped: %q", got[:40])
	}
	if !strings.Contains(got, "FAIL: TestX assertion mismatch") {
		t.Error("tail verdict dropped")
	}
	if !strings.Contains(got, "bytes elided") {
		t.Error("no elision note")
	}
}

// The cut points back off to line boundaries, so the elision note never lands
// mid-line and the surviving text stays readable.
func TestClampCutsOnLineBoundaries(t *testing.T) {
	var b strings.Builder
	for range 4000 {
		b.WriteString("0123456789abcdef0123456789abcdef\n")
	}
	got := Clamp(b.String(), 4*1024, "")
	i := strings.Index(got, "\n\n… [")
	if i < 0 {
		t.Fatal("no elision note")
	}
	if i > 0 && got[i-1] == 'f' && got[i] != '\n' {
		t.Errorf("head cut mid-line near %q", got[i-8:i])
	}
	_, after, ok := strings.Cut(got, "] …\n\n")
	if !ok {
		t.Fatal("no elision note close")
	}
	if len(after) > 0 && !strings.HasSuffix("0123456789abcdef0123456789abcdef\n", after[len(after)-33:]) {
		t.Errorf("tail does not start on a line boundary: %q", after[:16])
	}
}

func TestClampAdviceAppended(t *testing.T) {
	s := strings.Repeat("x\n", 4096)
	got := Clamp(s, 1024, "; narrow the command for the rest")
	if !strings.Contains(got, "bytes elided; narrow the command for the rest] …") {
		t.Errorf("advice missing from note: %q", got)
	}
}
