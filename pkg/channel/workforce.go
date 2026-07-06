package channel

import (
	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/curator"
	"github.com/tamnd/tomo/pkg/policy"
)

// DefaultWorker is the name of the always-present worker: tomo itself. A
// deployment with no specialists configured routes everything here.
const DefaultWorker = "tomo"

// Workforce resolves an inbound message to the worker that should handle it and
// builds that worker's agent, policy, and curator. A worker is a named
// specialist with its own persona, memory, and gate; the default worker is
// tomo. The router owns none of this itself, so how workers are configured
// stays out of pkg/channel.
type Workforce interface {
	// Route picks the worker for a message. An explicit @name prefix wins, then
	// a channel:chat binding, otherwise the default worker. It returns the
	// worker name and the text with any @name prefix stripped.
	Route(channel, chat, text string) (worker, cleaned string)
	// Agent builds a fresh agent for one worker, so its system prompt reflects
	// the worker's current memory.
	Agent(worker string) (*agent.Agent, error)
	// Engine returns the policy engine for one worker.
	Engine(worker string) *policy.Engine
	// Curator returns the reflection pass for one worker, or nil if none runs.
	Curator(worker string) *curator.Curator
	// Names lists every known worker, the default included.
	Names() []string
}

// AgentFunc returns a ready base agent, minus its gate. It runs once per
// message so the system prompt reflects the current memory index.
type AgentFunc func() (*agent.Agent, error)

// Solo is the one-worker workforce: everything routes to the default worker. It
// wraps the pieces the router would otherwise hold directly, so a deployment
// with no specialists behaves exactly as a single agent. cur may be nil.
func Solo(newAgent AgentFunc, engine *policy.Engine, cur *curator.Curator) Workforce {
	return &solo{newAgent: newAgent, engine: engine, cur: cur}
}

type solo struct {
	newAgent AgentFunc
	engine   *policy.Engine
	cur      *curator.Curator
}

func (s *solo) Route(_, _, text string) (string, string) { return DefaultWorker, text }
func (s *solo) Agent(string) (*agent.Agent, error)       { return s.newAgent() }
func (s *solo) Engine(string) *policy.Engine             { return s.engine }
func (s *solo) Curator(string) *curator.Curator          { return s.cur }
func (s *solo) Names() []string                          { return []string{DefaultWorker} }
