// Package skill loads markdown skills that teach tomo a workflow. A skill is a
// directory holding SKILL.md: YAML frontmatter (name, description, and a
// mandatory permission manifest) followed by the instructions themselves. The
// convention matches Agent Skills, so a SKILL.md written for that ecosystem
// loads here.
//
// Skills are plain files the user can read and edit, and nothing installs them
// but the user: there is no remote hub and no auto-install. A skill enters the
// system prompt as a one-line index entry; its body loads on demand when the
// agent reaches for it. A skill that cannot be parsed, or that omits its
// permission manifest, fails closed: it is left out of the index entirely.
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Permissions is the capability manifest every skill must declare. It names the
// classes of action the skill's workflow expects to take, mirroring the policy
// engine's classes. An empty manifest declares a skill that only reasons over
// what is already in the conversation.
type Permissions struct {
	Read  bool `yaml:"read"`
	Net   bool `yaml:"net"`
	Write bool `yaml:"write"`
	Exec  bool `yaml:"exec"`
}

// Skill is one loaded workflow.
type Skill struct {
	Name        string
	Description string
	Permissions Permissions
	Body        string
	Dir         string
}

// Store roots the skills under one directory, one subdirectory per skill.
type Store struct {
	Dir string
}

const (
	skillFile    = "SKILL.md"
	disabledFile = "disabled"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// frontmatter is the YAML head of a SKILL.md.
type frontmatter struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	Permissions *Permissions `yaml:"permissions"`
}

// List returns every enabled, well-formed skill, sorted by name. Skills that
// fail to parse or lack a manifest are skipped here so a bad skill never
// reaches the prompt; use Lint to see why one was rejected.
func (s *Store) List() ([]Skill, error) {
	all, err := s.scanAll()
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, r := range all {
		if r.Err == nil && r.Enabled {
			out = append(out, r.Skill)
		}
	}
	return out, nil
}

// Index is the one-line-per-skill block that rides in the system prompt. It is
// empty when no skills are installed.
func (s *Store) Index() (string, error) {
	skills, err := s.List()
	if err != nil {
		return "", err
	}
	if len(skills) == 0 {
		return "", nil
	}
	var b strings.Builder
	for _, sk := range skills {
		fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
	}
	return b.String(), nil
}

// Read returns one enabled skill's body by name, for loading on demand.
func (s *Store) Read(name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("bad skill name %q: want lowercase letters, digits, dashes", name)
	}
	sk, err := s.load(filepath.Join(s.Dir, name))
	if err != nil {
		return "", err
	}
	if s.disabled(name) {
		return "", fmt.Errorf("skill %q is disabled", name)
	}
	return sk.Body, nil
}

// load parses one skill directory. It fails closed: a missing manifest is an
// error, not a silently permissionless skill.
func (s *Store) load(dir string) (Skill, error) {
	raw, err := os.ReadFile(filepath.Join(dir, skillFile))
	if err != nil {
		return Skill{}, err
	}
	fm, body, err := parse(string(raw))
	if err != nil {
		return Skill{}, err
	}
	name := filepath.Base(dir)
	if fm.Name != name {
		return Skill{}, fmt.Errorf("skill %q: frontmatter name %q does not match its directory", name, fm.Name)
	}
	if !nameRe.MatchString(name) {
		return Skill{}, fmt.Errorf("skill %q: name must be lowercase letters, digits, dashes", name)
	}
	if strings.TrimSpace(fm.Description) == "" {
		return Skill{}, fmt.Errorf("skill %q: description is empty", name)
	}
	if fm.Permissions == nil {
		return Skill{}, fmt.Errorf("skill %q: no permissions manifest (declare read/net/write/exec)", name)
	}
	return Skill{
		Name:        name,
		Description: strings.TrimSpace(fm.Description),
		Permissions: *fm.Permissions,
		Body:        strings.TrimSpace(body),
		Dir:         dir,
	}, nil
}

// parse splits a SKILL.md into its YAML frontmatter and markdown body. The
// frontmatter is delimited by a leading --- line and a closing --- line.
func parse(text string) (frontmatter, string, error) {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return frontmatter{}, "", fmt.Errorf("missing frontmatter: a SKILL.md starts with a --- line")
	}
	head, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return frontmatter{}, "", fmt.Errorf("unterminated frontmatter: no closing --- line")
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
		return frontmatter{}, "", fmt.Errorf("frontmatter is not valid YAML: %w", err)
	}
	return fm, strings.TrimPrefix(body, "\n"), nil
}

func (s *Store) disabled(name string) bool {
	_, err := os.Stat(filepath.Join(s.Dir, name, disabledFile))
	return err == nil
}

// report is one skill directory's load outcome, used by List and Lint.
type report struct {
	Name    string
	Skill   Skill
	Enabled bool
	Err     error
}

// scanAll loads every subdirectory of the skills root, recording load errors
// rather than failing the whole scan.
func (s *Store) scanAll() ([]report, error) {
	entries, err := os.ReadDir(s.Dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []report
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		r := report{Name: name, Enabled: !s.disabled(name)}
		r.Skill, r.Err = s.load(filepath.Join(s.Dir, name))
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
