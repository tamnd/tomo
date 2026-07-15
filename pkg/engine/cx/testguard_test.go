package cx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitRepo builds a throwaway repo with the given committed files and returns its
// path, skipping the test when git is not on PATH.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	for name, body := range files {
		put(t, dir, name, body)
	}
	run("add", "-A")
	run("commit", "-qm", "base")
	return dir
}

func put(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// weakensTests must catch the shape the codex engine drove into on smolagents: a
// source change plus an existing test cut down to hide the regression it caused.
// A source change that updates a test's expected value in place must pass through.
func TestWeakensTests(t *testing.T) {
	base := map[string]string{
		"identity.py":      "def check():\n    return 1\n",
		"test_identity.py": "def test_check():\n    assert check() == 2\n",
	}

	t.Run("weakened the test alongside a source fix", func(t *testing.T) {
		dir := gitRepo(t, base)
		put(t, dir, "identity.py", "def check():\n    return 3\n")
		put(t, dir, "test_identity.py", "def test_check():\n    check()\n")
		if !weakensTests(dir) {
			t.Fatal("want fire: an existing test lost its assertion alongside a source change")
		}
	})

	t.Run("updated the test's expected value alongside a source fix", func(t *testing.T) {
		dir := gitRepo(t, base)
		put(t, dir, "identity.py", "def check():\n    return 3\n")
		put(t, dir, "test_identity.py", "def test_check():\n    assert check() == 3\n")
		if weakensTests(dir) {
			t.Fatal("must not fire: same assertion count, only the expected value moved")
		}
	})

	t.Run("only tests changed, code unfixed", func(t *testing.T) {
		dir := gitRepo(t, base)
		put(t, dir, "test_identity.py", "def test_check():\n    assert check() == 1\n")
		if !weakensTests(dir) {
			t.Fatal("want fire: only the test changed")
		}
	})

	t.Run("added a brand new test", func(t *testing.T) {
		dir := gitRepo(t, base)
		put(t, dir, "test_more.py", "def test_more():\n    assert True\n")
		if weakensTests(dir) {
			t.Fatal("must not fire: new coverage is legitimate")
		}
	})
}
