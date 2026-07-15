package cx

import (
	"os/exec"
	"path"
	"strings"
)

// testNudge is fed back once, as a user turn, when the model tries to end a
// coding turn having weakened or removed an existing test. It is a nudge, not a
// wall: if editing the test really was the job, the model reads it, recognises
// it, and ends the turn again on the next pass.
const testNudge = "You are ending the turn having weakened or deleted an existing test. " +
	"If your change made that test fail, the code under test is still wrong, and cutting the test's checks proves nothing: " +
	"restore the test to what it was and fix the source so the original test passes. " +
	"If the behaviour genuinely changed and updating the test reflects that, disregard this and finish."

// weakensTests reports whether the git working tree at dir ends the turn having
// weakened a pre-existing test. Two shapes count as weakening. First, an existing
// test edited or deleted while no source file changed: the fingerprint of
// "fixing" a failing test by rewriting the test rather than the code under test.
// Second, an existing test whose assertions were cut even though source did
// change too: the subtler dodge where a wrong source change breaks a test and the
// test is quietly weakened to hide the regression rather than the change being
// corrected. A brand new test file never counts, since adding coverage is
// legitimate, and updating a test in place with the same number of assertions
// (new expected values for genuinely changed behaviour) does not count either.
// Anything that is not a git repo, or where git is missing, reports false, so the
// gate never fires on a guess.
func weakensTests(dir string) bool {
	if dir == "" {
		return false
	}
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "-z").Output()
	if err != nil {
		return false
	}
	var editedTests, deletedTest, sourceChanged bool
	var modifiedTests []string
	for ent := range strings.SplitSeq(string(out), "\x00") {
		// Porcelain -z entries are "XY path"; a bare trailing field is the old
		// name of a rename, which we treat conservatively below.
		if len(ent) < 4 {
			continue
		}
		x, y, p := ent[0], ent[1], ent[3:]
		// A rename or copy could move code in or out of a test path; stay
		// conservative and count it as a source change, never a test edit.
		if x == 'R' || y == 'R' || x == 'C' || y == 'C' {
			sourceChanged = true
			continue
		}
		if isTestPath(p) {
			switch {
			case x == 'D' || y == 'D':
				deletedTest, editedTests = true, true
			case x == 'M' || y == 'M':
				editedTests = true
				modifiedTests = append(modifiedTests, p)
			}
			continue
		}
		sourceChanged = true
	}
	if !editedTests {
		return false
	}
	// Deleting a test, or touching only tests while the code stays unfixed, is
	// always the dodge.
	if deletedTest || !sourceChanged {
		return true
	}
	// Source changed too, so this may be a legitimate update. It is only the dodge
	// if a test now checks strictly less than it did.
	for _, p := range modifiedTests {
		if assertionsDropped(dir, p) {
			return true
		}
	}
	return false
}

// assertionsDropped reports whether the uncommitted diff of the test file at rel
// under dir removes more assertion-bearing lines than it adds, so the test now
// checks less than it did. It is a best-effort read across ecosystems: it counts
// the assertion keywords the common test frameworks use, and a genuine in-place
// update that swaps an expected value for another leaves the count unchanged and
// does not trip it.
func assertionsDropped(dir, rel string) bool {
	out, err := exec.Command("git", "-C", dir, "diff", "--unified=0", "--", rel).Output()
	if err != nil {
		return false
	}
	removed, added := 0, 0
	for line := range strings.SplitSeq(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case '-':
			if strings.HasPrefix(line, "---") {
				continue
			}
			if isAssertion(line[1:]) {
				removed++
			}
		case '+':
			if strings.HasPrefix(line, "+++") {
				continue
			}
			if isAssertion(line[1:]) {
				added++
			}
		}
	}
	return removed > added
}

// isAssertion reports whether a source line carries a test assertion. It matches
// the keywords the mainstream frameworks share: bare Python assert and the
// unittest/xUnit assertEquals family (all contain "assert"), the expect and
// raises spellings, testify's require, and Go's t.Error / t.Fatal.
func isAssertion(line string) bool {
	s := strings.TrimSpace(line)
	if s == "" {
		return false
	}
	for _, kw := range []string{"assert", "expect(", "raises(", "t.Error", "t.Fatal", "require.", ".should", "verify("} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// isTestPath recognises a test file by the conventions of the common ecosystems:
// directory names like tests/ or __tests__/, and filename shapes like
// foo_test.go, test_foo.py, foo.spec.ts, foo_spec.rb, FooTest.java.
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
