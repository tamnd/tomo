package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Doc is a skill in memory, before it is written to disk. The curator builds
// one when it drafts a skill from a workflow it saw; the CLI never does, since
// a human writing a skill just edits the file.
type Doc struct {
	Name        string
	Description string
	Permissions Permissions
	Body        string
}

// Write renders a Doc into a SKILL.md and validates that it loads cleanly, so a
// malformed draft never lands on disk. It overwrites an existing directory of
// the same name, which is what a curator re-drafting a skill wants. This is how
// the curator writes into the drafts store; installed skills are only ever
// created by the user editing files or by Promote.
func (s *Store) Write(d Doc) error {
	if !nameRe.MatchString(d.Name) {
		return fmt.Errorf("bad skill name %q: want lowercase letters, digits, dashes", d.Name)
	}
	if strings.TrimSpace(d.Description) == "" {
		return fmt.Errorf("skill %q: description is empty", d.Name)
	}
	content, err := render(d)
	if err != nil {
		return err
	}
	dir := filepath.Join(s.Dir, d.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, skillFile), []byte(content), 0o644); err != nil {
		return err
	}
	// Fail closed: if what we just wrote will not load, do not leave it behind.
	if _, err := s.load(dir); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("drafted skill %q does not load: %w", d.Name, err)
	}
	return nil
}

// Remove deletes a skill directory. The CLI uses it to discard a draft.
func (s *Store) Remove(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("bad skill name %q", name)
	}
	dir := filepath.Join(s.Dir, name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no skill %q", name)
	}
	return os.RemoveAll(dir)
}

// Promote moves a draft from one store into another: the explicit step that
// turns a proposal into an installed skill. It validates the draft loads
// cleanly first and refuses to clobber an installed skill of the same name, so
// a broken or colliding proposal never quietly replaces working instructions.
// Nothing but a deliberate user act calls this.
func Promote(from, to *Store, name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("bad skill name %q", name)
	}
	src := filepath.Join(from.Dir, name)
	if _, err := from.load(src); err != nil {
		return fmt.Errorf("draft %q will not load, not installing: %w", name, err)
	}
	dst := filepath.Join(to.Dir, name)
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("a skill named %q is already installed; remove it first", name)
	}
	if err := os.MkdirAll(to.Dir, 0o755); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

// render turns a Doc into SKILL.md text: YAML frontmatter then the body.
func render(d Doc) (string, error) {
	perms := d.Permissions
	fm := frontmatter{Name: d.Name, Description: strings.TrimSpace(d.Description), Permissions: &perms}
	head, err := yaml.Marshal(fm)
	if err != nil {
		return "", err
	}
	return "---\n" + string(head) + "---\n\n" + strings.TrimSpace(d.Body) + "\n", nil
}
