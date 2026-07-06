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
	"sync"
)

// Memory roots the store at one directory. It is safe for concurrent use: the
// curator writes on its own goroutine while a live turn may be reading.
type Memory struct {
	Dir string

	mu sync.RWMutex
}

const indexFile = "MEMORY.md"

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Index returns MEMORY.md, or "" when there is no memory yet.
func (m *Memory) Index() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	raw, err := os.ReadFile(filepath.Join(m.Dir, slug+".md"))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Provenance labels where a fact came from, so a later reader can weigh it: a
// fact the user stated outright is worth more than one the curator inferred
// while tidying up. It rides as a trailing line in the topic file, visible to
// the model when it reads the topic. The zero value adds no line.
type Provenance struct {
	Source string // who recorded it, e.g. "curator"
	From   string // where it was learned, e.g. "telegram:12345"
	On     string // date, supplied by the caller so memory stays clockless
}

func (p Provenance) line() string {
	if p.Source == "" {
		return ""
	}
	parts := []string{"source: " + p.Source}
	if p.From != "" {
		parts = append(parts, "from "+p.From)
	}
	if p.On != "" {
		parts = append(parts, p.On)
	}
	return "_" + strings.Join(parts, ", ") + "_"
}

// Save writes (or overwrites) a topic file and keeps the index line for it
// current: one line per slug, replaced in place when the fact changes.
func (m *Memory) Save(slug, title, body string) error {
	return m.SaveNoted(slug, title, body, Provenance{})
}

// SaveNoted is Save with a provenance stamp appended to the body. The curator
// uses it so an inferred fact carries where it came from; a direct write from
// the user leaves the stamp empty and trusts the fact plainly.
func (m *Memory) SaveNoted(slug, title, body string, p Provenance) error {
	if !slugRe.MatchString(slug) {
		return fmt.Errorf("bad memory slug %q: want lowercase letters, digits, dashes", slug)
	}
	if strings.TrimSpace(title) == "" {
		return fmt.Errorf("memory %q: title is empty", slug)
	}
	body = strings.TrimSpace(body)
	if line := p.line(); line != "" {
		body += "\n\n" + line
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return err
	}
	content := "# " + strings.TrimSpace(title) + "\n\n" + body + "\n"
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
