package oi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	workspaceFingerprintPrefix = "workspace-sha256:"
	workspaceHashBytes         = 8 << 20
	workspaceHashFiles         = 10_000
)

// fingerprintWorkspace gives a non-git workspace the same change signal that
// git status provides to a repository. Metadata covers the whole bounded walk;
// file contents are hashed up to a shared cap so a same-size rewrite is still
// visible without repeatedly reading an arbitrarily large tree.
func fingerprintWorkspace(root string) (string, error) {
	h := sha256.New()
	remaining := int64(workspaceHashBytes)
	files := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != root && entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".tomodata") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		files++
		if files > workspaceHashFiles {
			return fmt.Errorf("workspace has more than %d files", workspaceHashFiles)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00%d\n", rel, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if !info.Mode().IsRegular() || remaining <= 0 {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(h, io.LimitReader(f, remaining))
		closeErr := f.Close()
		remaining -= n
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return "", err
	}
	return workspaceFingerprintPrefix + hex.EncodeToString(h.Sum(nil)), nil
}
