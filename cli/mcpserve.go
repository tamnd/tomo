package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/config"
	"github.com/tamnd/tomo/pkg/mcp"
	"github.com/tamnd/tomo/pkg/memory"
	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/provider"
	"github.com/tamnd/tomo/pkg/schedule"
	"github.com/tamnd/tomo/pkg/store"
	"github.com/tamnd/tomo/pkg/tool"
)

// newMCPCmd serves tomo itself as an MCP server on stdio, so Claude Code and
// other MCP clients can reach its chat, memory, and scheduling. Only JSON-RPC
// travels on stdout; nothing else may print there.
func newMCPCmd() *cobra.Command {
	var model string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve tomo's tools over MCP on stdio",
		Long: "mcp turns tomo into a Model Context Protocol server. Point an MCP\n" +
			"client at `tomo mcp` and it gains a chat tool that runs a full tomo\n" +
			"turn, tools to recall and store memory, and one to schedule later work.\n" +
			"Actions gated to ask are declined, since a server has no one to prompt.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			st, err := store.Open(filepath.Join(cfg.DataDir, "tomo.db"))
			if err != nil {
				return err
			}
			defer st.Close()

			engine := policy.New(policy.Config{
				Read: cfg.Policy.Read, Net: cfg.Policy.Net,
				Write: cfg.Policy.Write, Exec: cfg.Policy.Exec, Rules: cfg.Policy.Rules,
			})
			if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
				return err
			}
			auditor, err := policy.OpenFileAuditor(filepath.Join(cfg.DataDir, "audit.log"))
			if err != nil {
				return err
			}
			defer auditor.Close()
			guard := policy.NewGuard(engine, denyApprover{}, auditor)

			tools, err := tomoTools(cfg, model, guard, st)
			if err != nil {
				return err
			}
			srv := mcp.NewServer("tomo", tools)
			return srv.Serve(cmd.Context(), os.Stdin, os.Stdout)
		},
	}
	cmd.Flags().StringVarP(&model, "model", "m", "", "provider/model for the chat tool (default from config)")
	return cmd
}

// tomoTools is the set exposed over MCP: a chat tool backed by a full agent,
// the memory tools, and a scheduler bound to an mcp session.
func tomoTools(cfg *config.Config, model string, guard agent.Gate, st *store.Store) ([]tool.Tool, error) {
	a, _, err := buildAgent(cfg, agentBuild{model: model}, guard)
	if err != nil {
		return nil, err
	}
	mem := &memory.Memory{Dir: filepath.Join(cfg.DataDir, "memory")}

	tools := []tool.Tool{chatTool(a)}
	tools = append(tools, mem.Tools()...)
	tools = append(tools, schedule.Tool(st, "mcp", "default"))
	return tools, nil
}

// chatTool runs one full tomo turn for the given message and returns the reply
// text. It is stateless: each call starts fresh, so the calling client owns the
// surrounding conversation.
func chatTool(a *agent.Agent) tool.Tool {
	return tool.Tool{
		Name: "tomo_chat",
		Description: "Ask tomo. Runs a full agent turn with tomo's own tools and memory " +
			"and returns its reply. Each call is independent; carry any context in the message.",
		Class: tool.ClassRead,
		Schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"message": {"type": "string", "description": "what to ask or tell tomo"}
			},
			"required": ["message"]
		}`),
		Run: func(ctx context.Context, input json.RawMessage) (string, error) {
			var v struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(input, &v); err != nil {
				return "", err
			}
			v.Message = strings.TrimSpace(v.Message)
			if v.Message == "" {
				return "", fmt.Errorf("message is required")
			}
			var buf strings.Builder
			turn := *a // shallow copy so a stray field write never races a concurrent call
			if _, err := turn.Turn(ctx, nil, provider.UserText(v.Message), &collectSink{&buf}); err != nil {
				return "", err
			}
			return strings.TrimSpace(buf.String()), nil
		},
	}
}

// collectSink gathers streamed assistant text and drops tool activity, which
// the MCP client neither sees nor needs.
type collectSink struct{ b *strings.Builder }

func (s *collectSink) Text(t string)                           { s.b.WriteString(t) }
func (s *collectSink) ToolStart(string, json.RawMessage)       {}
func (s *collectSink) ToolEnd(name, result string, isErr bool) {}

// denyApprover refuses every ask. An MCP server has no interactive prompt, so
// anything gated to ask fails closed, matching how background runs behave.
type denyApprover struct{}

func (denyApprover) Approve(context.Context, policy.Request) (bool, error) { return false, nil }
