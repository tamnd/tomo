package cli

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestRenderChannelIsValidGo(t *testing.T) {
	body, err := renderChannel("google_chat")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	src := string(body)

	// It must parse as Go.
	if _, err := parser.ParseFile(token.NewFileSet(), "google_chat.go", src, parser.AllErrors); err != nil {
		t.Fatalf("generated code does not parse: %v\n%s", err, src)
	}

	// The name underscores must map to an exported CamelCase type.
	for _, want := range []string{
		"package google_chat",
		`channel.Register("google_chat", driver{})`,
		"type GoogleChat struct",
		`func (c *GoogleChat) Name() string { return "google_chat" }`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated code missing %q", want)
		}
	}
}

func TestExportName(t *testing.T) {
	cases := map[string]string{
		"matrix":      "Matrix",
		"google_chat": "GoogleChat",
		"ms_teams":    "MsTeams",
		"webex":       "Webex",
	}
	for in, want := range cases {
		if got := exportName(in); got != want {
			t.Errorf("exportName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidName(t *testing.T) {
	good := []string{"matrix", "google_chat", "x2", "a"}
	bad := []string{"", "2fast", "Matrix", "with-dash", "with space"}
	for _, s := range good {
		if !validName.MatchString(s) {
			t.Errorf("validName rejected good name %q", s)
		}
	}
	for _, s := range bad {
		if validName.MatchString(s) {
			t.Errorf("validName accepted bad name %q", s)
		}
	}
}
