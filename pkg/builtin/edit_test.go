package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditReplacesUniqueSnippet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")
	if err := os.WriteFile(path, []byte("package p\n\nfunc A() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ed := editTool(dir)
	out, err := ed.Run(context.Background(), mustJSON(map[string]any{
		"path": "f.go", "old_string": "return 1", "new_string": "return 2",
	}))
	if err != nil || !strings.Contains(out, "replaced 1") {
		t.Fatalf("edit: %q %v", out, err)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "return 2") || strings.Contains(string(got), "return 1") {
		t.Errorf("file not edited: %s", got)
	}
}

func TestEditRejectsNonUnique(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ed := editTool(dir)
	_, err := ed.Run(context.Background(), mustJSON(map[string]any{
		"path": "f.txt", "old_string": "x", "new_string": "y",
	}))
	if err == nil || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("expected non-unique error, got %v", err)
	}
	// replace_all makes it succeed and change both.
	out, err := ed.Run(context.Background(), mustJSON(map[string]any{
		"path": "f.txt", "old_string": "x", "new_string": "y", "replace_all": true,
	}))
	if err != nil || !strings.Contains(out, "replaced 2") {
		t.Fatalf("replace_all: %q %v", out, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "y\ny\n" {
		t.Errorf("replace_all wrong: %q", got)
	}
}

func TestEditErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ed := editTool(dir)
	cases := []struct {
		name           string
		old, new, want string
	}{
		{"empty old", "", "x", "old_string is empty"},
		{"identical", "hello", "hello", "identical"},
		{"not found", "nope", "x", "not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ed.Run(context.Background(), mustJSON(map[string]any{
				"path": "f.txt", "old_string": c.old, "new_string": c.new,
			}))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("want error %q, got %v", c.want, err)
			}
		})
	}
}
