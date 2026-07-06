// Package memory is tomo's long-term store: MEMORY.md is a one-line-per-fact
// index that rides in the system prompt, and each fact's detail lives in its
// own markdown topic file. Plain files on purpose, so the user can read,
// edit, and grep their agent's memory like anything else on disk.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Memory roots the store at one directory.
type Memory struct {
	Dir string
}

const indexFile = "MEMORY.md"

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Index returns MEMORY.md, or "" when there is no memory yet.
func (m *Memory) Index() (string, error) {
	raw, err := os.ReadFile(filepath.Join(m.Dir, indexFile))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Read returns one topic file's content.
func (m *Memory) Read(slug string) (string, error) {
	if !slugRe.MatchString(slug) {
		return "", fmt.Errorf("bad memory slug %q: want lowercase letters, digits, dashes", slug)
	}
	raw, err := os.ReadFile(filepath.Join(m.Dir, slug+".md"))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Save writes (or overwrites) a topic file and keeps the index line for it
// current: one line per slug, replaced in place when the fact changes.
func (m *Memory) Save(slug, title, body string) error {
	if !slugRe.MatchString(slug) {
		return fmt.Errorf("bad memory slug %q: want lowercase letters, digits, dashes", slug)
	}
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("memory %q: title is empty", slug)
	}
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return err
	}
	content := "# " + strings.TrimSpace(title) + "\n\n" + strings.TrimSpace(body) + "\n"
	if err := os.WriteFile(filepath.Join(m.Dir, slug+".md"), []byte(content), 0o644); err != nil {
		return err
	}
	return m.updateIndex(slug, strings.TrimSpace(title))
}

func (m *Memory) updateIndex(slug, title string) error {
	path := filepath.Join(m.Dir, indexFile)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	line := fmt.Sprintf("- [%s](%s.md): %s", slug, slug, title)
	marker := fmt.Sprintf("- [%s](", slug)

	var lines []string
	replaced := false
	for l := range strings.SplitSeq(strings.TrimRight(string(existing), "\n"), "\n") {
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, marker) {
			lines = append(lines, line)
			replaced = true
			continue
		}
		lines = append(lines, l)
	}
	if !replaced {
		lines = append(lines, line)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
