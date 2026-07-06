// Package tool holds the registry of actions the agent may take. Every tool
// declares a capability class so the policy layer can gate whole categories
// without knowing individual tools.
package tool

import (
	"context"
	"encoding/json"

	"github.com/tamnd/tomo/pkg/provider"
)

// Class is what a tool is allowed to touch, from the policy engine's point of
// view. One class per tool, the strongest thing it does.
type Class string

const (
	ClassRead  Class = "read"  // reads local state
	ClassNet   Class = "net"   // talks to the network
	ClassWrite Class = "write" // mutates local state
	ClassExec  Class = "exec"  // runs arbitrary code
)

// Tool is one callable action.
type Tool struct {
	Name        string
	Description string
	Class       Class
	Schema      json.RawMessage
	Run         func(ctx context.Context, input json.RawMessage) (string, error)
}

// Registry is an ordered set of tools. The zero value and the nil pointer
// are both empty, usable registries.
type Registry struct {
	tools  []*Tool
	byName map[string]*Tool
}

// NewRegistry builds a registry from tools, keeping order.
func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{}
	for _, t := range tools {
		r.Add(t)
	}
	return r
}

// Add appends a tool, replacing any existing tool of the same name.
func (r *Registry) Add(t Tool) {
	if r.byName == nil {
		r.byName = map[string]*Tool{}
	}
	if old, ok := r.byName[t.Name]; ok {
		*old = t
		return
	}
	cp := t
	r.tools = append(r.tools, &cp)
	r.byName[t.Name] = &cp
}

// Get looks a tool up by name.
func (r *Registry) Get(name string) (*Tool, bool) {
	if r == nil {
		return nil, false
	}
	t, ok := r.byName[name]
	return t, ok
}

// Defs renders the registry the way a model request wants it.
func (r *Registry) Defs() []provider.Tool {
	if r == nil {
		return nil
	}
	out := make([]provider.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, provider.Tool{Name: t.Name, Description: t.Description, Schema: t.Schema})
	}
	return out
}
