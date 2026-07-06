package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write lays down a skill directory with the given SKILL.md content.
func write(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const goodSkill = `---
name: pr-review
description: Review a pull request and flag risks
permissions:
  read: true
  net: true
---
Fetch the diff, read it, and list anything risky.
`

func TestLoadAndIndex(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pr-review", goodSkill)
	s := &Store{Dir: root}

	skills, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 {
		t.Fatalf("skills = %+v", skills)
	}
	sk := skills[0]
	if sk.Name != "pr-review" || sk.Description != "Review a pull request and flag risks" {
		t.Errorf("skill = %+v", sk)
	}
	if !sk.Permissions.Read || !sk.Permissions.Net || sk.Permissions.Exec || sk.Permissions.Write {
		t.Errorf("permissions = %+v", sk.Permissions)
	}

	idx, err := s.Index()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(idx, "pr-review: Review a pull request") {
		t.Errorf("index = %q", idx)
	}

	body, err := s.Read("pr-review")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Fetch the diff") {
		t.Errorf("body = %q", body)
	}
}

func TestNoSkillsIsEmpty(t *testing.T) {
	s := &Store{Dir: filepath.Join(t.TempDir(), "missing")}
	idx, err := s.Index()
	if err != nil || idx != "" {
		t.Fatalf("index = %q err %v", idx, err)
	}
	skills, err := s.List()
	if err != nil || len(skills) != 0 {
		t.Fatalf("skills = %v err %v", skills, err)
	}
}

func TestManifestIsMandatory(t *testing.T) {
	root := t.TempDir()
	write(t, root, "no-manifest", `---
name: no-manifest
description: missing its permissions block
---
do a thing
`)
	s := &Store{Dir: root}
	// A skill without a manifest fails closed: absent from the index.
	if idx, _ := s.Index(); idx != "" {
		t.Errorf("skill without manifest leaked into index: %q", idx)
	}
	if _, err := s.Read("no-manifest"); err == nil {
		t.Error("Read of a manifestless skill should fail")
	}
}

func TestNameMustMatchDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, "actual-dir", `---
name: claims-otherwise
description: name does not match its home
permissions: {}
---
body
`)
	if _, err := (&Store{Dir: root}).Read("actual-dir"); err == nil {
		t.Error("mismatched name should fail to load")
	}
}

func TestEnableDisable(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pr-review", goodSkill)
	s := &Store{Dir: root}

	if err := s.Disable("pr-review"); err != nil {
		t.Fatal(err)
	}
	if idx, _ := s.Index(); idx != "" {
		t.Errorf("disabled skill still in index: %q", idx)
	}
	if _, err := s.Read("pr-review"); err == nil {
		t.Error("reading a disabled skill should fail")
	}

	if err := s.Enable("pr-review"); err != nil {
		t.Fatal(err)
	}
	if idx, _ := s.Index(); !strings.Contains(idx, "pr-review") {
		t.Errorf("re-enabled skill missing from index: %q", idx)
	}

	if err := s.Disable("nope"); err == nil {
		t.Error("disabling a missing skill should fail")
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, c := range []string{
		"no frontmatter at all",
		"---\nname: x\n", // unterminated
		"---\n: : bad yaml :\n---\nbody",
	} {
		if _, _, err := parse(c); err == nil {
			t.Errorf("parse(%q) should have failed", c)
		}
	}
}
