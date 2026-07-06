package skill

import (
	"fmt"
	"regexp"
	"strings"
)

// Finding is one problem the scanner has with a skill. An error means the skill
// should not be trusted as written; a warning is worth a look but not fatal.
type Finding struct {
	Skill   string
	Level   string // "error" or "warn"
	Message string
}

// Entry is a skill's listing state, whether or not it loaded cleanly. The CLI
// uses it to show everything on disk, including the broken and the disabled.
type Entry struct {
	Name        string
	Description string
	Permissions Permissions
	Enabled     bool
	Err         error
}

// Entries lists every skill directory with its load state, sorted by name.
func (s *Store) Entries() ([]Entry, error) {
	reports, err := s.scanAll()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(reports))
	for _, r := range reports {
		e := Entry{Name: r.Name, Enabled: r.Enabled, Err: r.Err}
		if r.Err == nil {
			e.Description = r.Skill.Description
			e.Permissions = r.Skill.Permissions
		}
		out = append(out, e)
	}
	return out, nil
}

// Lint scans every skill and reports what is wrong: skills that fail to load,
// skills whose body reaches for a capability the manifest does not declare, and
// skills carrying hidden instructions. A clean store returns no findings.
func (s *Store) Lint() ([]Finding, error) {
	reports, err := s.scanAll()
	if err != nil {
		return nil, err
	}
	var findings []Finding
	for _, r := range reports {
		if r.Err != nil {
			findings = append(findings, Finding{Skill: r.Name, Level: "error", Message: r.Err.Error()})
			continue
		}
		findings = append(findings, scan(r.Skill)...)
	}
	return findings, nil
}

var (
	urlRe         = regexp.MustCompile(`https?://`)
	execFenceRe   = regexp.MustCompile("(?i)```+\\s*(sh|bash|zsh|shell|console)\\b")
	shellPromptRe = regexp.MustCompile(`(?m)^\s*\$\s+\S`)
	injectionRe   = regexp.MustCompile(`(?i)ignore (all |the )?(previous|prior|above) (instructions|prompt)|disregard (your|the) (instructions|system prompt)|forget (your|the) (instructions|rules)`)
)

// scan runs the content checks on one loaded skill.
func scan(sk Skill) []Finding {
	var out []Finding
	add := func(msg string) { out = append(out, Finding{Skill: sk.Name, Level: "error", Message: msg}) }
	body := sk.Body

	if r := firstHiddenRune(body); r != 0 {
		add(fmt.Sprintf("hidden unicode in body (U+%04X); scrub zero-width and bidi characters", r))
	}
	if strings.Contains(body, "<!--") {
		add("HTML comment in body: instructions must be in plain view, not hidden in comments")
	}
	if injectionRe.MatchString(body) {
		add("body reads like a prompt injection (tells the agent to ignore its instructions)")
	}
	if !sk.Permissions.Net && usesNet(body) {
		add("body reaches the network but the manifest does not declare net")
	}
	if !sk.Permissions.Exec && usesExec(body) {
		add("body runs shell commands but the manifest does not declare exec")
	}
	return out
}

// firstHiddenRune returns the first zero-width or bidi-control rune in s, or 0.
// These are invisible to a human reading the file but not to the model, so they
// are a classic way to smuggle instructions into a skill.
func firstHiddenRune(s string) rune {
	for _, r := range s {
		switch {
		case r >= 0x200B && r <= 0x200F, // zero-width space through RLM
			r >= 0x202A && r <= 0x202E, // bidi embeddings and overrides
			r >= 0x2060 && r <= 0x2064, // word joiner and invisible operators
			r == 0xFEFF:                // zero-width no-break space (BOM)
			return r
		}
	}
	return 0
}

func usesNet(body string) bool {
	return urlRe.MatchString(body) || strings.Contains(body, "`fetch`")
}

func usesExec(body string) bool {
	return execFenceRe.MatchString(body) || shellPromptRe.MatchString(body) || strings.Contains(body, "`shell`")
}
