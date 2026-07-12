package agent

import (
	"os/exec"
	"path"
	"strings"
)

// testNudge is fed back once, as a user turn, when the model tries to end a
// coding turn having rewritten an existing test and changed no source. It is a
// nudge, not a wall: if editing the test really was the job, the model reads
// this, recognises it, and ends the turn again on the next pass.
const testNudge = "You are ending the turn having changed only existing test files and no source file. " +
	"If the job was to fix or change code, the code is still not fixed, and a test you rewrote to pass proves nothing: " +
	"restore that test to what it was and change the source under test instead. " +
	"If editing that test is genuinely what the user asked for, disregard this and finish."

// onlyTestsEdited reports whether the git working tree at dir has uncommitted
// changes that modify or delete existing test files while touching no source
// file. That is the fingerprint of "fixing" a failing test by rewriting the
// test rather than the code under test. Creating a brand new test file does not
// count: adding coverage is legitimate work, so a turn that only adds new tests
// is left alone. Anything that is not a git repo, or where git is missing,
// reports false, so the gate never fires on a guess.
func onlyTestsEdited(dir string) bool {
	if dir == "" {
		return false
	}
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "-z").Output()
	if err != nil {
		return false
	}
	var existingTestEdited, sourceChanged bool
	for ent := range strings.SplitSeq(string(out), "\x00") {
		// Porcelain -z entries are "XY path"; a bare trailing field is the old
		// name of a rename, which we treat conservatively below.
		if len(ent) < 4 {
			continue
		}
		x, y, p := ent[0], ent[1], ent[3:]
		// A rename or copy could move code in or out of a test path; stay
		// conservative and count it as a source change so the gate never fires.
		if x == 'R' || y == 'R' || x == 'C' || y == 'C' {
			sourceChanged = true
			continue
		}
		if isTestPath(p) {
			// Modified or deleted an existing test, staged or unstaged.
			if x == 'M' || y == 'M' || x == 'D' || y == 'D' {
				existingTestEdited = true
			}
			// Added or untracked test files (A, ??) are new coverage: ignore.
			continue
		}
		sourceChanged = true
	}
	return existingTestEdited && !sourceChanged
}

// isTestPath recognises a test file by the conventions of the common
// ecosystems: directory names like tests/ or __tests__/, and filename shapes
// like foo_test.go, test_foo.py, foo.spec.ts, foo_spec.rb, FooTest.java.
func isTestPath(p string) bool {
	p = strings.ToLower(path.Clean(strings.ReplaceAll(p, "\\", "/")))
	for seg := range strings.SplitSeq(p, "/") {
		switch seg {
		case "test", "tests", "testing", "__tests__", "spec", "specs":
			return true
		}
	}
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, "_test.py"),
		strings.Contains(base, ".test."),
		strings.Contains(base, ".spec."),
		strings.HasSuffix(base, "_spec.rb"),
		strings.HasSuffix(base, "_test.rb"),
		strings.HasSuffix(base, "test.java"),
		strings.HasSuffix(base, "tests.java"):
		return true
	}
	return false
}
