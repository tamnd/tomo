package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewModes(t *testing.T) {
	for _, mode := range []string{"", "none", "hako", "restricted", "standard", "net", "dev"} {
		if _, err := New(mode, ""); err != nil {
			t.Errorf("New(%q): %v", mode, err)
		}
	}
	if _, err := New("bogus", ""); err == nil {
		t.Error("New(bogus): want error, got nil")
	}
}

func TestNoneRuns(t *testing.T) {
	box, err := New("none", "")
	if err != nil {
		t.Fatal(err)
	}
	if box.Name() != "none" {
		t.Errorf("name = %q, want none", box.Name())
	}
	out, err := box.Run(context.Background(), []string{"sh", "-c", "echo hello"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("out = %q, want hello", out)
	}
}

func TestNoneReportsExitCode(t *testing.T) {
	box, _ := New("none", "")
	_, err := box.Run(context.Background(), []string{"sh", "-c", "exit 3"})
	if err == nil {
		t.Fatal("want error for non-zero exit, got nil")
	}
}

// TestConfinedRunsAndConfines exercises the real hako sandbox on the platforms
// that support it. It is the behavior test for Lesson 2: an allowed command
// runs, but a write outside the working tree is refused by the kernel, not by
// the model.
func TestConfinedRunsAndConfines(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("sandbox not supported on %s", runtime.GOOS)
	}
	box, err := New("standard", "")
	if err != nil {
		t.Fatalf("New(standard): %v", err)
	}

	// A command that only touches the working tree succeeds.
	out, err := box.Run(context.Background(), []string{"sh", "-c", "echo confined"})
	if err != nil {
		t.Skipf("sandbox could not start here (%v); skipping confinement check", err)
	}
	if !strings.Contains(out, "confined") {
		t.Errorf("out = %q, want it to contain confined", out)
	}

	// A write to the home directory, which standard does not grant, is refused.
	home, herr := os.UserHomeDir()
	if herr != nil {
		t.Skip("no home dir to test denial against")
	}
	target := filepath.Join(home, ".tomo-sandbox-should-not-exist")
	_, _ = box.Run(context.Background(), []string{"sh", "-c", "printf x > " + target})
	if _, statErr := os.Stat(target); statErr == nil {
		_ = os.Remove(target)
		t.Errorf("sandbox allowed a write to %s outside the working tree", target)
	}
}
