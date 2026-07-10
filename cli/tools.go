package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/tamnd/tomo/pkg/builtin"
	"github.com/tamnd/tomo/pkg/config"
	"github.com/tamnd/tomo/pkg/memory"
	"github.com/tamnd/tomo/pkg/sandbox"
	"github.com/tamnd/tomo/pkg/skill"
	"github.com/tamnd/tomo/pkg/tool"
)

// newToolsCmd is the capability catalog: what can tomo do, and how do I add
// more. It reads the same tool sets buildAgent assembles for a turn, so the
// list matches what the model actually gets and what the gate reasons over.
// There is no marketplace and nothing to install; discovery is over sources
// that are already in your config or compiled in.
func newToolsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "List the tools tomo can call, grouped by source",
		Long: "tools lists every action tomo can take, grouped by where it comes\n" +
			"from: the builtins compiled in, your memory and skill tools, and any\n" +
			"MCP server in your config. Each line shows the tool's capability class\n" +
			"(read, net, write, exec), which is what the policy gate keys on.\n\n" +
			"This is the same toolset a turn loads, so it answers \"what can it do\"\n" +
			"without guessing. Use 'tools search <term>' to filter, and\n" +
			"'tools attach' to add a source in one command.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runToolsList(cmd, "") },
	}
	cmd.AddCommand(newToolsSearchCmd(), newToolsAttachCmd())
	return cmd
}

func newToolsSearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search <term>",
		Short: "List only the tools whose name or description matches a term",
		Long: "search filters the catalog to the tools whose name or description\n" +
			"contains the term, case-insensitively. Ask the agent \"what tools do\n" +
			"you have for X\" and its answer should match 'tomo tools search X'.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runToolsList(cmd, args[0]) },
	}
}

// runToolsList renders the catalog, filtered to term when it is non-empty. MCP
// dialing notes are collected separately and printed after the listing so a
// failed server never breaks the table.
func runToolsList(cmd *cobra.Command, term string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	th := themeFor(out)
	var notes bytes.Buffer
	sources := catalog(cmd.Context(), cfg, &notes)
	term = strings.ToLower(strings.TrimSpace(term))

	total := 0
	for _, s := range sources {
		shown := s.Tools
		if term != "" {
			shown = filterTools(s.Tools, term)
		}
		if len(shown) == 0 {
			continue
		}
		// Align the name and class columns to the widest in this group so the
		// descriptions start on a common edge, measured in visible columns so
		// the color codes do not throw off the layout.
		nameW, classW := 0, 0
		for _, t := range shown {
			nameW = max(nameW, len(t.Name))
			classW = max(classW, len(t.Class))
		}
		fmt.Fprintf(out, "\n%s\n", th.source(s.Name, len(shown)))
		for _, t := range shown {
			name := padRight(th.name(t.Name), nameW)
			class := padRight(th.classBadge(t.Class), classW)
			desc := th.muted(summarizeDesc(t.Description, 68))
			fmt.Fprintf(out, "  %s   %s   %s\n", name, class, desc)
		}
		total += len(shown)
	}

	switch {
	case total == 0 && term != "":
		fmt.Fprintf(out, "%s\n", th.muted(fmt.Sprintf("no tools match %q", term)))
	case total == 0:
		fmt.Fprintf(out, "%s\n", th.muted("no tools registered"))
	default:
		summary := fmt.Sprintf("%d %s", total, plural(total, "tool", "tools"))
		if term != "" {
			summary += fmt.Sprintf(" matching %q", term)
		} else {
			summary += " · tomo tools search <term> to filter · tomo tools attach to add a source"
		}
		fmt.Fprintf(out, "\n%s\n", th.count(summary))
	}
	if notes.Len() > 0 {
		fmt.Fprintf(out, "\n%s\n%s", th.muted("notes"), th.muted(strings.TrimRight(notes.String(), "\n")))
		fmt.Fprintln(out)
	}
	return nil
}

// toolSource is one origin of tools and the tools it contributes, kept together
// so the catalog can show where a capability comes from. The gate reasons over
// the same tools once they are flattened into a registry; this only adds the
// provenance the flat registry does not carry.
type toolSource struct {
	Name  string // "builtin", "memory", "skill", or "mcp:<server>"
	Tools []tool.Tool
}

// catalog assembles every tool tomo would load for a turn, grouped by source.
// It builds the same builtin, memory, and skill sets buildAgent does, then
// dials each configured MCP server to list its tools. A server that will not
// start is noted and skipped, never fatal, so the rest still list.
func catalog(ctx context.Context, cfg *config.Config, notes io.Writer) []toolSource {
	box, _ := sandbox.New("none", cfg.Workspace)
	sources := []toolSource{
		{Name: "builtin", Tools: builtin.All(box, cfg.Workspace)},
		{Name: "memory", Tools: (&memory.Memory{Dir: filepath.Join(cfg.DataDir, "memory")}).Tools()},
		{Name: "skill", Tools: (&skill.Store{Dir: filepath.Join(cfg.DataDir, "skills")}).Tools()},
	}
	return append(sources, catalogMCP(ctx, cfg, notes)...)
}

// catalogMCP dials every configured MCP server, lists its tools, and closes it
// again. It mirrors dialMCP's skip-on-failure posture but groups per server and
// writes its status to notes rather than the listing, so a down server is a
// note under the table, not a hole in it.
func catalogMCP(ctx context.Context, cfg *config.Config, notes io.Writer) []toolSource {
	servers := cfg.MCP.Servers
	if len(servers) == 0 {
		return nil
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []toolSource
	for _, name := range names {
		c, err := dialServer(ctx, name, servers[name])
		if err != nil {
			fmt.Fprintf(notes, "  mcp %s: %v\n", name, err)
			continue
		}
		if c == nil {
			fmt.Fprintf(notes, "  mcp %s: no command or url, skipped\n", name)
			continue
		}
		if err := c.Initialize(ctx); err != nil {
			fmt.Fprintf(notes, "  mcp %s: handshake failed: %v\n", name, err)
			_ = c.Close()
			continue
		}
		ts, err := c.Tools(ctx, tool.ClassExec)
		if err != nil {
			fmt.Fprintf(notes, "  mcp %s: list tools failed: %v\n", name, err)
			_ = c.Close()
			continue
		}
		_ = c.Close()
		out = append(out, toolSource{Name: "mcp:" + name, Tools: ts})
	}
	return out
}

// filterTools keeps the tools whose name or description contains term, which is
// already lower-cased by the caller.
func filterTools(tools []tool.Tool, term string) []tool.Tool {
	var out []tool.Tool
	for _, t := range tools {
		if strings.Contains(strings.ToLower(t.Name), term) ||
			strings.Contains(strings.ToLower(t.Description), term) {
			out = append(out, t)
		}
	}
	return out
}

// summarizeDesc collapses a description to a single line and caps its width so
// the catalog stays a scannable table.
func summarizeDesc(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		s = strings.TrimSpace(s[:limit]) + "…"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func newToolsAttachCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attach",
		Short: "Attach a new tool source to your config in one command",
		Long: "attach adds a tool source to your config so its tools join the next\n" +
			"turn, without hand-editing YAML. Attached sources are not tomo's own\n" +
			"code, so they default to ask at the gate even when their class would\n" +
			"otherwise run; allow one you trust with a policy.rules entry.",
	}
	cmd.AddCommand(newToolsAttachMCPCmd())
	return cmd
}

func newToolsAttachMCPCmd() *cobra.Command {
	var command, url string
	var toolArgs, env, headers []string
	cmd := &cobra.Command{
		Use:   "mcp <name>",
		Short: "Add an MCP server to your config so its tools join the toolset",
		Long: "attach mcp writes an mcp.servers.<name> block into your config. Give\n" +
			"--command (with --arg/--env) for a local stdio server, or --url (with\n" +
			"--header) for a remote HTTP one. The change is validated by reloading\n" +
			"the config; if it would not parse, the original is restored.\n\n" +
			"Quote a ${VAR} so the shell does not expand it before it is written:\n" +
			"  tomo tools attach mcp files --command mcp-server-filesystem --arg ~/work\n" +
			"  tomo tools attach mcp gh --command npx --arg -y --arg @modelcontextprotocol/server-github --env 'GITHUB_TOKEN=${GITHUB_TOKEN}'",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.ToLower(strings.TrimSpace(args[0]))
			if !validName.MatchString(name) {
				return fmt.Errorf("server name %q: use lower-case letters, digits, and underscores, starting with a letter", name)
			}
			switch {
			case command == "" && url == "":
				return fmt.Errorf("give --command (a local stdio server) or --url (a remote HTTP server)")
			case command != "" && url != "":
				return fmt.Errorf("--command and --url are mutually exclusive: a server is either local or remote")
			}
			envMap, err := keyVals(env, "--env")
			if err != nil {
				return err
			}
			headerMap, err := keyVals(headers, "--header")
			if err != nil {
				return err
			}
			server := config.MCPServer{
				Command: command,
				Args:    toolArgs,
				Env:     envMap,
				URL:     url,
				Headers: headerMap,
			}

			path, err := configPath(cmd)
			if err != nil {
				return err
			}
			if err := attachMCPServer(path, name, server); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "attached mcp server %q to %s\n\n", name, path)
			fmt.Fprintf(out, "next:\n")
			fmt.Fprintf(out, "  1. tomo tools           # confirm its tools show up under mcp:%s\n", name)
			fmt.Fprintf(out, "  2. its tools default to ask at the gate; allow ones you trust in policy.rules\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&command, "command", "", "executable for a local stdio server")
	cmd.Flags().StringArrayVar(&toolArgs, "arg", nil, "an argument to the command (repeatable, in order)")
	cmd.Flags().StringArrayVar(&env, "env", nil, "KEY=VALUE environment for the command (repeatable)")
	cmd.Flags().StringVar(&url, "url", "", "endpoint for a remote HTTP server")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "KEY=VALUE header for a remote server (repeatable)")
	return cmd
}

// attachMCPServer inserts an mcp.servers.<name> block into the config file at
// path, preserving the rest of the document, then reloads to confirm it parses.
// If the write would not load, the original file is put back and the error is
// returned, so a bad attach never leaves a broken config behind.
func attachMCPServer(path, name string, s config.MCPServer) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top level is not a mapping", path)
	}
	servers := ensureMap(ensureMap(root, "mcp"), "servers")
	if mapValue(servers, name) != nil {
		return fmt.Errorf("mcp server %q is already in %s; pick another name or edit it by hand", name, path)
	}
	servers.Content = append(servers.Content, scalarNode(name), serverNode(s))

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	_ = enc.Close()

	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return err
	}
	if _, err := config.Load(path); err != nil {
		_ = os.WriteFile(path, raw, 0o600)
		return fmt.Errorf("attach produced a config that would not load, restored the original: %w", err)
	}
	return nil
}

// serverNode renders one MCPServer as a YAML mapping node, emitting only the
// fields that are set so a stdio server does not carry empty url/headers keys
// and vice versa.
func serverNode(s config.MCPServer) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	add := func(k string, v *yaml.Node) { n.Content = append(n.Content, scalarNode(k), v) }
	if s.Command != "" {
		add("command", scalarNode(s.Command))
	}
	if len(s.Args) > 0 {
		add("args", seqNode(s.Args))
	}
	if len(s.Env) > 0 {
		add("env", strMapNode(s.Env))
	}
	if s.URL != "" {
		add("url", scalarNode(s.URL))
	}
	if len(s.Headers) > 0 {
		add("headers", strMapNode(s.Headers))
	}
	return n
}

func scalarNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

func seqNode(vals []string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, v := range vals {
		n.Content = append(n.Content, scalarNode(v))
	}
	return n
}

func strMapNode(m map[string]string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		n.Content = append(n.Content, scalarNode(k), scalarNode(m[k]))
	}
	return n
}

// mapValue returns the value node for key in a mapping node, or nil if absent.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// ensureMap returns the mapping node under key, creating an empty one if the
// key is absent, so a config with no mcp section grows one cleanly.
func ensureMap(parent *yaml.Node, key string) *yaml.Node {
	if v := mapValue(parent, key); v != nil {
		return v
	}
	val := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, scalarNode(key), val)
	return val
}

// keyVals parses repeated KEY=VALUE flag values into a map, naming the flag in
// the error so a malformed pair points at the fix.
func keyVals(pairs []string, flag string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("%s %q: want KEY=VALUE", flag, p)
		}
		out[k] = v
	}
	return out, nil
}

// configPath resolves the config file the commands read and write: the --config
// flag when set, otherwise the default location.
func configPath(cmd *cobra.Command) (string, error) {
	path, err := cmd.Flags().GetString("config")
	if err != nil {
		return "", err
	}
	if path == "" {
		return config.DefaultPath()
	}
	return path, nil
}
