package skill

import (
	"fmt"
	"os"
	"path/filepath"
)

// Enable clears a skill's disabled marker so it loads again.
func (s *Store) Enable(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("bad skill name %q", name)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, name)); err != nil {
		return fmt.Errorf("no skill %q", name)
	}
	err := os.Remove(filepath.Join(s.Dir, name, disabledFile))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Disable drops a marker file in the skill's directory so the loader skips it.
// The skill stays on disk; it just no longer rides in the prompt.
func (s *Store) Disable(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("bad skill name %q", name)
	}
	dir := filepath.Join(s.Dir, name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no skill %q", name)
	}
	return os.WriteFile(filepath.Join(dir, disabledFile), []byte("disabled by tomo skills disable\n"), 0o644)
}
