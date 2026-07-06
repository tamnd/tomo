// Package policy is the fail-closed gate every tool call passes through. A
// decision comes from three things: the tool's capability class, any per-tool
// override, and whether the session has ingested untrusted outside content.
// The default posture is conservative on purpose; this is the difference
// between an agent that helps and one that runs whatever a fetched web page
// told it to.
package policy

import (
	"fmt"
	"strings"

	"github.com/tamnd/tomo/pkg/tool"
)

// Decision is what the engine says about a call.
type Decision string

const (
	Allow Decision = "allow" // run it, no questions
	Ask   Decision = "ask"   // run it only if the user approves
	Deny  Decision = "deny"  // never run it
)

// ParseDecision reads a decision from config, defaulting unknown or empty
// values to Ask so a typo fails closed rather than open.
func ParseDecision(s string) Decision {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return Allow
	case "deny":
		return Deny
	default:
		return Ask
	}
}

// Config is the policy section of the config file. Class defaults set the
// baseline; Rules override by exact tool name and win over the class default.
type Config struct {
	Read  string            `yaml:"read"`
	Net   string            `yaml:"net"`
	Write string            `yaml:"write"`
	Exec  string            `yaml:"exec"`
	Rules map[string]string `yaml:"rules"`
}

// Engine evaluates decisions. The zero value is not useful; build with New.
type Engine struct {
	class map[tool.Class]Decision
	rules map[string]Decision
}

// New builds an engine from config, filling any unset class with the safe
// default: reads and network calls run, writes and code execution ask.
func New(cfg Config) *Engine {
	e := &Engine{
		class: map[tool.Class]Decision{
			tool.ClassRead:  orDefault(cfg.Read, Allow),
			tool.ClassNet:   orDefault(cfg.Net, Allow),
			tool.ClassWrite: orDefault(cfg.Write, Ask),
			tool.ClassExec:  orDefault(cfg.Exec, Ask),
		},
		rules: map[string]Decision{},
	}
	for name, dec := range cfg.Rules {
		e.rules[name] = ParseDecision(dec)
	}
	return e
}

func orDefault(s string, def Decision) Decision {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return ParseDecision(s)
}

// Decide returns the decision for one call. tainted is true once the session
// has pulled in untrusted external content; in that state a write or exec that
// would normally run is escalated to ask, because the model's instructions may
// no longer be entirely the user's. A per-tool rule still wins: an explicit
// allow or deny is the user's considered choice and is not second-guessed.
func (e *Engine) Decide(name string, class tool.Class, tainted bool) (Decision, string) {
	if dec, ok := e.rules[name]; ok {
		return dec, fmt.Sprintf("rule for %q", name)
	}
	dec := e.class[class]
	if dec == "" {
		// An unknown class is not something we reasoned about: fail closed.
		return Ask, fmt.Sprintf("unknown capability %q", class)
	}
	if tainted && dec == Allow && (class == tool.ClassWrite || class == tool.ClassExec) {
		return Ask, fmt.Sprintf("%s escalated: session touched untrusted content", class)
	}
	return dec, string(class) + " default"
}
