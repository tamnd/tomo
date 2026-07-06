package cli

import (
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/channel"
	"github.com/tamnd/tomo/pkg/config"
	"github.com/tamnd/tomo/pkg/curator"
	"github.com/tamnd/tomo/pkg/policy"
	"github.com/tamnd/tomo/pkg/tool"
)

// buildWorkforce assembles the set of workers serve routes between. With no
// workers configured it is the plain single-agent Solo, so a simple deployment
// behaves exactly as before. With specialists it is a multiForce that routes by
// @name prefix or channel binding and gives each worker its own memory, gate,
// and curator.
func buildWorkforce(cfg *config.Config, model string, mcpTools []tool.Tool) (channel.Workforce, error) {
	engine := buildEngine(cfg.Policy, mcpTools)
	newAgent := func() (*agent.Agent, error) {
		a, _, err := buildAgent(cfg, agentBuild{model: model}, nil, mcpTools...)
		return a, err
	}
	cur, err := buildCurator(cfg, curatorBuild{model: model})
	if err != nil {
		return nil, err
	}
	if len(cfg.Workers) == 0 {
		return channel.Solo(newAgent, engine, cur), nil
	}

	m := &multiForce{
		workers:  map[string]*worker{},
		bindings: map[string]string{},
	}
	m.workers[channel.DefaultWorker] = &worker{
		name:     channel.DefaultWorker,
		newAgent: newAgent,
		engine:   engine,
		cur:      cur,
	}
	for name, wc := range cfg.Workers {
		if name == channel.DefaultWorker {
			return nil, fmt.Errorf("worker %q: reserved name, that is the default worker", name)
		}
		w, err := buildWorker(cfg, name, wc, model, mcpTools)
		if err != nil {
			return nil, err
		}
		m.workers[name] = w
		for _, key := range wc.Channels {
			if other, taken := m.bindings[key]; taken {
				return nil, fmt.Errorf("channel %q is bound to both %q and %q", key, other, name)
			}
			m.bindings[key] = name
		}
	}
	return m, nil
}

// buildWorker resolves one specialist: its own persona, model, policy, and a
// memory subtree of its own under <data>/workers/<name>, so nothing it learns
// leaks into another worker's prompt.
func buildWorker(cfg *config.Config, name string, wc config.Worker, defaultModel string, mcpTools []tool.Tool) (*worker, error) {
	model := wc.Model
	if model == "" {
		model = defaultModel
	}
	root := filepath.Join(cfg.DataDir, "workers", name)
	memDir := filepath.Join(root, "memory")
	skillsDir := filepath.Join(root, "skills")
	draftsDir := filepath.Join(root, "skill-drafts")
	persona := wc.Persona

	newAgent := func() (*agent.Agent, error) {
		a, _, err := buildAgent(cfg, agentBuild{
			persona:   persona,
			model:     model,
			memoryDir: memDir,
			skillsDir: skillsDir,
		}, nil, mcpTools...)
		return a, err
	}
	cur, err := buildCurator(cfg, curatorBuild{
		model:     model,
		memoryDir: memDir,
		skillsDir: skillsDir,
		draftsDir: draftsDir,
	})
	if err != nil {
		return nil, err
	}
	return &worker{
		name:     name,
		newAgent: newAgent,
		engine:   buildEngine(mergePolicy(cfg.Policy, wc.Policy), mcpTools),
		cur:      cur,
	}, nil
}

// buildEngine builds a policy engine and marks every MCP tool external, so a
// tool that is not tomo's own code defaults to ask even when its class runs.
func buildEngine(p config.Policy, mcpTools []tool.Tool) *policy.Engine {
	e := policy.New(policy.Config{
		Read: p.Read, Net: p.Net, Write: p.Write, Exec: p.Exec, Rules: p.Rules,
	})
	for _, t := range mcpTools {
		e.MarkExternal(t.Name)
	}
	return e
}

// mergePolicy layers a worker's policy over the top-level one: an unset field
// falls back to the default, and rules merge with the worker's winning.
func mergePolicy(base, over config.Policy) config.Policy {
	out := base
	if over.Read != "" {
		out.Read = over.Read
	}
	if over.Net != "" {
		out.Net = over.Net
	}
	if over.Write != "" {
		out.Write = over.Write
	}
	if over.Exec != "" {
		out.Exec = over.Exec
	}
	if len(over.Rules) > 0 {
		out.Rules = map[string]string{}
		maps.Copy(out.Rules, base.Rules)
		maps.Copy(out.Rules, over.Rules)
	}
	return out
}

// worker holds the pieces the workforce hands back for one specialist. The
// agent is built fresh per message so its prompt reflects the latest memory;
// the engine and curator are stable for the life of the daemon.
type worker struct {
	name     string
	newAgent func() (*agent.Agent, error)
	engine   *policy.Engine
	cur      *curator.Curator
}

// multiForce is the CLI workforce with named specialists. It routes a message
// to a worker by an explicit @name prefix, then by channel binding, otherwise
// to the default worker tomo.
type multiForce struct {
	workers  map[string]*worker
	bindings map[string]string // channel:chat -> worker name
}

func (m *multiForce) Route(ch, chat, text string) (string, string) {
	if name, rest, ok := atName(text); ok {
		if _, known := m.workers[name]; known {
			return name, rest
		}
	}
	if name, ok := m.bindings[ch+":"+chat]; ok {
		return name, text
	}
	return channel.DefaultWorker, text
}

func (m *multiForce) Agent(name string) (*agent.Agent, error) {
	w, ok := m.workers[name]
	if !ok {
		return nil, fmt.Errorf("no worker %q", name)
	}
	return w.newAgent()
}

func (m *multiForce) Engine(name string) *policy.Engine {
	if w, ok := m.workers[name]; ok {
		return w.engine
	}
	return nil
}

func (m *multiForce) Curator(name string) *curator.Curator {
	if w, ok := m.workers[name]; ok {
		return w.cur
	}
	return nil
}

func (m *multiForce) Names() []string {
	names := make([]string, 0, len(m.workers))
	for name := range m.workers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// atName pulls a leading "@name" off a message. It returns the name, the rest
// of the text with the prefix and following space removed, and whether one was
// present. A bare "@name" with no message routes there with empty text.
func atName(text string) (name, rest string, ok bool) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "@") {
		return "", "", false
	}
	t = t[1:]
	if i := strings.IndexAny(t, " \t\n"); i >= 0 {
		return t[:i], strings.TrimSpace(t[i:]), true
	}
	return t, "", true
}
