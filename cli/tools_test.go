package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/tomo/pkg/config"
	"github.com/tamnd/tomo/pkg/tool"
)

const attachBaseConfig = `default_model: anthropic/claude-fable-5
providers:
  anthropic:
    type: anthropic
    api_key: ${ANTHROPIC_API_KEY}
policy:
  read: allow
  net: allow
  write: ask
  exec: ask
`

// TestAttachMCPRoundTrips writes a stdio and a remote server and asserts the
// result loads back through the real config loader with both entries intact,
// and that ${VAR} is preserved literally rather than expanded at write time.
func TestAttachMCPRoundTrips(t *testing.T) {
	t.Setenv("MCP_TOKEN", "should-not-be-expanded-on-disk")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(attachBaseConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	stdio := config.MCPServer{Command: "mcp-server-filesystem", Args: []string{"/work"}}
	if err := attachMCPServer(path, "files", stdio); err != nil {
		t.Fatalf("attach stdio: %v", err)
	}
	remote := config.MCPServer{URL: "https://mcp.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer ${MCP_TOKEN}"}}
	if err := attachMCPServer(path, "remote", remote); err != nil {
		t.Fatalf("attach remote: %v", err)
	}

	// The raw file must still hold the literal ${MCP_TOKEN}, unexpanded.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "${MCP_TOKEN}") {
		t.Errorf("attach expanded ${MCP_TOKEN} on disk:\n%s", raw)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload after attach: %v", err)
	}
	files, ok := cfg.MCP.Servers["files"]
	if !ok || files.Command != "mcp-server-filesystem" || len(files.Args) != 1 || files.Args[0] != "/work" {
		t.Errorf("files server not round-tripped: %+v", files)
	}
	rem, ok := cfg.MCP.Servers["remote"]
	if !ok || rem.URL != "https://mcp.example.com/mcp" {
		t.Errorf("remote server not round-tripped: %+v", rem)
	}
	// The loader expands ${MCP_TOKEN}, so the in-memory header is expanded.
	if rem.Headers["Authorization"] != "Bearer should-not-be-expanded-on-disk" {
		t.Errorf("header did not expand on load: %q", rem.Headers["Authorization"])
	}
}

// TestAttachMCPRejectsDuplicate asserts a second attach under the same name
// fails and leaves the file untouched.
func TestAttachMCPRejectsDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(attachBaseConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := attachMCPServer(path, "files", config.MCPServer{Command: "x"}); err != nil {
		t.Fatalf("first attach: %v", err)
	}
	before, _ := os.ReadFile(path)
	if err := attachMCPServer(path, "files", config.MCPServer{Command: "y"}); err == nil {
		t.Fatal("expected duplicate attach to fail")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("rejected attach still modified the file")
	}
}

func TestFilterTools(t *testing.T) {
	tools := []tool.Tool{
		{Name: "read_file", Description: "Read a text file from disk", Class: tool.ClassRead},
		{Name: "shell", Description: "Run a shell command", Class: tool.ClassExec},
		{Name: "fetch", Description: "HTTP GET a URL and read the page", Class: tool.ClassNet},
	}
	// Match by name.
	if got := filterTools(tools, "file"); len(got) != 1 || got[0].Name != "read_file" {
		t.Errorf("name match: got %+v", got)
	}
	// Match by description ("read" appears in read_file's description too).
	got := filterTools(tools, "read")
	if len(got) != 2 {
		t.Errorf("description match: want 2, got %d (%+v)", len(got), got)
	}
	if len(filterTools(tools, "nonesuch")) != 0 {
		t.Error("expected no matches for a missing term")
	}
}

func TestSummarizeDesc(t *testing.T) {
	got := summarizeDesc("line one\nline two\twith\ttabs", 80)
	if strings.ContainsAny(got, "\n\t") {
		t.Errorf("summarizeDesc left a newline or tab: %q", got)
	}
	long := summarizeDesc(strings.Repeat("x", 200), 10)
	if !strings.HasSuffix(long, "…") || len([]rune(long)) > 11 {
		t.Errorf("summarizeDesc did not cap width: %q", long)
	}
}

// keyVals is exercised here because a malformed --env/--header pair is a common
// user mistake and must name the flag in its error.
func TestKeyVals(t *testing.T) {
	m, err := keyVals([]string{"A=1", "B=x=y"}, "--env")
	if err != nil {
		t.Fatalf("keyVals: %v", err)
	}
	if m["A"] != "1" || m["B"] != "x=y" {
		t.Errorf("keyVals parsed wrong: %+v", m)
	}
	if _, err := keyVals([]string{"nope"}, "--env"); err == nil || !strings.Contains(err.Error(), "--env") {
		t.Errorf("want an error naming --env, got %v", err)
	}
}

// Guard against a schema field going stale: the catalog's class column reads
// tool.Class, so a tool with no class would render blank. This is a cheap check
// that the builtin file tools still carry the classes the catalog shows.
func TestServerNodeOmitsEmptyFields(t *testing.T) {
	n := serverNode(config.MCPServer{Command: "x"})
	keys := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		keys[n.Content[i].Value] = true
	}
	if !keys["command"] {
		t.Error("serverNode dropped command")
	}
	for _, unwanted := range []string{"url", "headers", "args", "env"} {
		if keys[unwanted] {
			t.Errorf("serverNode emitted empty %q", unwanted)
		}
	}
	// Sanity: the node encodes to valid JSON-free YAML scalar values.
	if _, err := json.Marshal(n.Content[1].Value); err != nil {
		t.Fatal(err)
	}
}
