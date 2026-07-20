package oi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFingerprintWorkspaceDetectsSameSizeRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "solution.py")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := fingerprintWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after!"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := fingerprintWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("same-size rewrite did not change workspace fingerprint")
	}
}

func TestDirtyPathsIgnoresWorkspaceFingerprint(t *testing.T) {
	if paths := dirtyPaths(workspaceFingerprintPrefix + "abc"); len(paths) != 0 {
		t.Fatalf("fingerprint parsed as dirty paths: %v", paths)
	}
}
