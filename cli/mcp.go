package cli

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/tamnd/tomo/pkg/config"
	"github.com/tamnd/tomo/pkg/mcp"
	"github.com/tamnd/tomo/pkg/tool"
)

// dialMCP launches every configured MCP server, runs the handshake, and
// collects their tools. It returns the tools plus a closer that shuts the
// servers down. A server that fails to start is reported and skipped, so one
// bad entry never takes the daemon down. Tools land as ClassExec since they run
// outside tomo's own trust boundary; the policy engine still gates them.
func dialMCP(ctx context.Context, cfg *config.Config, out io.Writer) ([]tool.Tool, func()) {
	servers := cfg.MCP.Servers
	if len(servers) == 0 {
		return nil, func() {}
	}

	// Dial in a stable order so status lines read the same each run.
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	var tools []tool.Tool
	var clients []*mcp.Client
	for _, name := range names {
		s := servers[name]
		c, err := dialServer(ctx, name, s)
		if err != nil {
			fmt.Fprintf(out, "  mcp %s: %v\n", name, err)
			continue
		}
		if c == nil {
			fmt.Fprintf(out, "  mcp %s: no command or url, skipped\n", name)
			continue
		}
		if err := c.Initialize(ctx); err != nil {
			fmt.Fprintf(out, "  mcp %s: handshake failed: %v\n", name, err)
			_ = c.Close()
			continue
		}
		ts, err := c.Tools(ctx, tool.ClassExec)
		if err != nil {
			fmt.Fprintf(out, "  mcp %s: list tools failed: %v\n", name, err)
			_ = c.Close()
			continue
		}
		clients = append(clients, c)
		tools = append(tools, ts...)
		fmt.Fprintf(out, "  mcp %s: %d tools\n", name, len(ts))
	}

	closer := func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}
	return tools, closer
}

// dialServer starts one server over whichever transport it names: a url means
// HTTP, a command means a local stdio subprocess. It returns nil, nil when the
// entry names neither.
func dialServer(ctx context.Context, name string, s config.MCPServer) (*mcp.Client, error) {
	switch {
	case s.URL != "":
		return mcp.StartHTTP(name, s.URL, s.Headers), nil
	case s.Command != "":
		return mcp.StartStdio(ctx, name, s.Command, s.Args, s.Env)
	default:
		return nil, nil
	}
}
