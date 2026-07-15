package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIsTestPath(t *testing.T) {
	yes := []string{
		"pkg/agent/agent_test.go",
		"src/test_identity.py",
		"src/identity_test.py",
		"tests/helpers.py",
		"foo/__tests__/bar.js",
		"web/button.spec.ts",
		"web/button.test.tsx",
		"spec/models/user_spec.rb",
		"lib/user_test.rb",
		"src/main/java/AppTest.java",
	}
	for _, p := range yes {
		if !isTestPath(p) {
			t.Errorf("isTestPath(%q) = false, want true", p)
		}
	}
	no := []string{
		"pkg/agent/agent.go",
		"src/identity.py",
		"README.md",
		"lib/user.rb",
		"web/button.ts",
		"src/main/java/App.java",
		"contest/latest.py", // "test" is a substring, not a path segment
	}
	for _, p := range no {
		if isTestPath(p) {
			t.Errorf("isTestPath(%q) = true, want false", p)
		}
	}
}

// gitRepo builds a throwaway repo with the given committed files and returns
// its path. It skips the test when git is not on PATH.
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
		write(t, dir, name, body)
	}
	run("add", "-A")
	run("commit", "-qm", "base")
	return dir
}

// write drops a file under dir. A nil t means the caller is inside a scripted
// tool where the path is known-good, so any error panics rather than failing a
// test.
func write(t *testing.T, dir, name, body string) {
	if t != nil {
		t.Helper()
	}
	full := filepath.Join(dir, name)
	fail := func(err error) {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		fail(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		fail(err)
	}
}

func TestWeakensTests(t *testing.T) {
	base := map[string]string{
		"identity.py":      "def check():\n    return 1\n",
		"test_identity.py": "def test_check():\n    assert check() == 2\n",
	}

	t.Run("rewrote an existing test, no source", func(t *testing.T) {
		dir := gitRepo(t, base)
		write(t, dir, "test_identity.py", "def test_check():\n    assert check() == 1\n")
		if !weakensTests(dir) {
			t.Fatal("want fire: only the test changed")
		}
	})

	t.Run("fixed the source and updated the test in place", func(t *testing.T) {
		dir := gitRepo(t, base)
		write(t, dir, "identity.py", "def check():\n    return 2\n")
		write(t, dir, "test_identity.py", "def test_check():\n    assert check() == 2  # touched\n")
		if weakensTests(dir) {
			t.Fatal("must not fire: source changed and the test keeps its assertion")
		}
	})

	t.Run("fixed the source but weakened the test to hide a regression", func(t *testing.T) {
		// The smolagents shape: source changed, and an existing test that its
		// change broke was cut down instead of the change being corrected.
		dir := gitRepo(t, base)
		write(t, dir, "identity.py", "def check():\n    return 3\n")
		write(t, dir, "test_identity.py", "def test_check():\n    check()\n")
		if !weakensTests(dir) {
			t.Fatal("want fire: an existing test lost its assertion alongside a source change")
		}
	})

	t.Run("fixed the source and repointed the test's expected value", func(t *testing.T) {
		dir := gitRepo(t, base)
		write(t, dir, "identity.py", "def check():\n    return 3\n")
		write(t, dir, "test_identity.py", "def test_check():\n    assert check() == 3\n")
		if weakensTests(dir) {
			t.Fatal("must not fire: same assertion count, only the expected value moved")
		}
	})

	t.Run("added a brand new test", func(t *testing.T) {
		dir := gitRepo(t, base)
		write(t, dir, "test_more.py", "def test_more():\n    assert True\n")
		if weakensTests(dir) {
			t.Fatal("must not fire: new coverage is legitimate")
		}
	})

	t.Run("only source changed", func(t *testing.T) {
		dir := gitRepo(t, base)
		write(t, dir, "identity.py", "def check():\n    return 2\n")
		if weakensTests(dir) {
			t.Fatal("must not fire: no test touched")
		}
	})

	t.Run("nothing changed", func(t *testing.T) {
		dir := gitRepo(t, base)
		if weakensTests(dir) {
			t.Fatal("must not fire on a clean tree")
		}
	})

	t.Run("deleted an existing test", func(t *testing.T) {
		dir := gitRepo(t, base)
		if err := os.Remove(filepath.Join(dir, "test_identity.py")); err != nil {
			t.Fatal(err)
		}
		if !weakensTests(dir) {
			t.Fatal("want fire: deleting a test to make the suite pass is the same trick")
		}
	})

	t.Run("not a git repo", func(t *testing.T) {
		if weakensTests(t.TempDir()) {
			t.Fatal("must not fire outside a git repo")
		}
	})

	t.Run("empty workspace", func(t *testing.T) {
		if weakensTests("") {
			t.Fatal("must not fire with no workspace")
		}
	})
}
