// Package orch is the planning and orchestration layer from spec 2080/ostres: a
// layer above the turn loop that turns a job into a plan, runs the plan's steps
// under a budget through the same gate, checks each against a grounded
// postcondition, and reports honestly. The turn loop (pkg/agent) is unchanged;
// orch only wraps it.
package orch

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Postcondition is a step's grounded success check. A step is done because a
// check holds, not because a model said so. The rungs are ordered from most
// grounded (a file parses, a command exits zero) to least (a produced result
// mentions a token); the design prefers the mechanical rungs and avoids leaning
// on the model to grade its own work.
type Postcondition struct {
	// Kind selects the check. The zero value ("" or "none") always passes, for
	// a step whose success genuinely cannot be checked mechanically.
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"` // for file_exists, file_contains
	Text string `json:"text,omitempty"` // for file_contains, result_contains
	Cmd  string `json:"cmd,omitempty"`  // for shell_zero
}

// Postcondition kinds.
const (
	PostNone           = "none"
	PostResultNonEmpty = "result_nonempty"
	PostResultContains = "result_contains"
	PostFileExists     = "file_exists"
	PostFileContains   = "file_contains"
	PostShellZero      = "shell_zero"
)

// ParsePostcondition reads a postcondition from its stored JSON. An empty or
// malformed value is treated as the always-passing none check rather than an
// error, so a step with no postcondition simply is not gated on one.
func ParsePostcondition(s string) Postcondition {
	if strings.TrimSpace(s) == "" {
		return Postcondition{Kind: PostNone}
	}
	var p Postcondition
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return Postcondition{Kind: PostNone}
	}
	if p.Kind == "" {
		p.Kind = PostNone
	}
	return p
}

// JSON renders a postcondition to its stored form.
func (p Postcondition) JSON() string {
	b, err := json.Marshal(p)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Evaluable reports whether this postcondition is one the orchestrator knows how
// to check, used by plan validation so a plan cannot ship a check that will
// never evaluate.
func (p Postcondition) Evaluable() bool {
	switch p.Kind {
	case PostNone, PostResultNonEmpty, PostResultContains, PostFileExists, PostFileContains, PostShellZero:
		return true
	default:
		return false
	}
}

// Eval runs the check and returns whether it held and a one-line reason. All
// file paths resolve inside the workspace, and shell_zero runs in the workspace
// directory, so a postcondition cannot reach outside the job's working tree.
// result is the step's produced output, for the result_* rungs.
func (p Postcondition) Eval(ctx context.Context, workspace, result string) (bool, string) {
	switch p.Kind {
	case "", PostNone:
		return true, "no postcondition"
	case PostResultNonEmpty:
		if strings.TrimSpace(result) == "" {
			return false, "result is empty"
		}
		return true, "result is non-empty"
	case PostResultContains:
		if !strings.Contains(result, p.Text) {
			return false, "result does not mention " + p.Text
		}
		return true, "result mentions " + p.Text
	case PostFileExists:
		if _, err := os.Stat(inWorkspace(workspace, p.Path)); err != nil {
			return false, p.Path + " does not exist"
		}
		return true, p.Path + " exists"
	case PostFileContains:
		b, err := os.ReadFile(inWorkspace(workspace, p.Path))
		if err != nil {
			return false, "cannot read " + p.Path
		}
		if !strings.Contains(string(b), p.Text) {
			return false, p.Path + " does not contain " + p.Text
		}
		return true, p.Path + " contains " + p.Text
	case PostShellZero:
		cmd := exec.CommandContext(ctx, "sh", "-c", p.Cmd)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			return false, p.Cmd + " exited non-zero: " + firstLine(string(out))
		}
		return true, p.Cmd + " exited zero"
	default:
		return false, "unknown postcondition " + p.Kind
	}
}

// inWorkspace joins a relative path under the workspace, and keeps an absolute
// or escaping path from reaching outside it by falling back to the base name.
func inWorkspace(workspace, path string) string {
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		clean = filepath.Base(clean)
	}
	return filepath.Join(workspace, clean)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	line, _, _ := strings.Cut(s, "\n")
	return line
}
