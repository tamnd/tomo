package orch

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/tamnd/tomo/pkg/provider"
)

// StepSpec is a planned step before it becomes ledger rows: the schema the
// planner produces and the orchestrator consumes. Idx is the step's position in
// the plan; Deps and #E<idx> placeholders reference earlier positions.
type StepSpec struct {
	Goal     string            `json:"goal"`
	Deps     []int             `json:"deps"`
	Inputs   map[string]string `json:"inputs"`
	Executor string            `json:"executor"`
	Post     Postcondition     `json:"postcondition"`
}

// Planner decides whether a request is a job and, if so, turns it into a plan.
// It prefers a template (no model call, correct by construction), falls back to
// one planning model call for a novel job, and validates the result before it
// ever runs, since the model cannot be trusted to check its own plan.
type Planner struct {
	Provider  provider.Provider
	Model     string
	MaxTokens int
	Tools     []string // tool names a `tool:<name>` executor may reference
	Workers   []string // worker names a `worker:<name>` executor may reference
}

var conjRE = regexp.MustCompile(`(?i)\b(and|then|after that)\b`)

// listItemRE matches a line that opens an enumerated task item: a bullet
// (-, *, •) or a number followed by a delimiter. Two or more of these is an
// explicit checklist, the clearest multi-deliverable shape a request can take.
var listItemRE = regexp.MustCompile(`(?m)^\s*(?:[-*•]|\d+[.)])\s+\S`)

// jobPhrases are unambiguous "this is a job" markers. A request that enumerates
// its deliverables ("do all of the following"), asks for a workflow, or asks to
// be worked step by step is asking for a plan, not a single answer.
var jobPhrases = []string{
	"for each", "each of", "do all of the following", "all of the following",
	"do the following", "step by step", "step-by-step", "workflow",
	"one at a time", "sub-agent", "subagent", "sub agents", "in parallel",
}

// jobVerbs are the imperative verbs whose repetition, joined by conjunctions,
// signals a multi-deliverable job rather than a single turn. Each is matched as
// a whole word with an optional inflection (fetch/fetches/fetched/fetching), so
// the noun "readings" does not count as the verb "read".
var jobVerbs = []string{
	"research", "write", "post", "summarize", "fetch", "read", "open",
	"build", "run", "test", "fix", "clean", "refactor", "reconcile", "review", "analyze",
}

// jobVerbRE holds one compiled matcher per verb, each accepting the base form or
// a common inflection at a word boundary.
var jobVerbRE = func() []*regexp.Regexp {
	res := make([]*regexp.Regexp, len(jobVerbs))
	for i, v := range jobVerbs {
		res[i] = regexp.MustCompile(`(?i)\b` + v + `(s|es|ed|ing)?\b`)
	}
	return res
}()

// itemStop are verbs that mark the tail action of a "research X and do Y"
// request, so a fragment carrying one is a clause, not a list item.
var itemStop = []string{"write", "compar", "post", "summar", "create", "open", "then", "build"}

// TriggerJob is the cheap, no-model structural signal from the spec: a request
// with an explicit multi-deliverable shape. Three signals fire it, in order of
// how unambiguous they are: an enumerated checklist of two or more items, an
// explicit job phrase ("do all of the following", "for each", "workflow"), or
// three or more distinct action verbs joined by conjunctions. It is deliberately
// biased toward "turn": a missed job just runs as one turn, while a false
// positive wastes planning tokens and can fragment a task that needed one
// context, so a single instruction with one or two verbs never fires (a bare
// "read X and write Y" stays a turn; the compound bar is three).
func TriggerJob(text string) bool {
	low := strings.ToLower(text)
	if len(listItemRE.FindAllString(text, 2)) >= 2 {
		return true
	}
	for _, p := range jobPhrases {
		if strings.Contains(low, p) {
			return true
		}
	}
	// The research-then-synthesize shape ("research these N and write a
	// comparison") is a job even at two verbs: it is exactly what the template
	// planner fans out, so it must reach the planner to be recognized.
	if strings.Contains(low, "research") && (strings.Contains(low, "compar") || strings.Contains(low, "write")) {
		return true
	}
	if !conjRE.MatchString(low) {
		return false
	}
	verbs := 0
	for _, re := range jobVerbRE {
		if re.MatchString(low) {
			verbs++
		}
	}
	return verbs >= 3
}

// Plan produces a validated plan for a goal, choosing the cheapest path that
// works. A goal with no usable plan falls back to a single turn step, so a job
// always at least runs as one turn rather than failing to start.
func (p *Planner) Plan(ctx context.Context, goal string) ([]StepSpec, error) {
	if specs := p.template(goal); specs != nil {
		if err := Validate(specs, p.Tools, p.Workers); err == nil {
			return specs, nil
		}
	}
	if p.Provider != nil {
		specs, err := p.planLLM(ctx, goal, "")
		if err == nil {
			if verr := Validate(specs, p.Tools, p.Workers); verr == nil {
				return specs, nil
			} else if specs2, err2 := p.planLLM(ctx, goal, verr.Error()); err2 == nil {
				if Validate(specs2, p.Tools, p.Workers) == nil {
					return specs2, nil
				}
			}
		}
	}
	return []StepSpec{{Goal: goal, Executor: "turn", Post: Postcondition{Kind: PostResultNonEmpty}}}, nil
}

var listSplitRE = regexp.MustCompile(`\s*(?:,|;| and | or |\n)\s*`)

// template matches the common "research/summarize each of these N things and
// compare them" shape and builds its DAG by construction: one gather step per
// item, one synthesis step depending on all of them. It returns nil when the
// request does not clearly match, deferring to the model planner.
func (p *Planner) template(goal string) []StepSpec {
	low := strings.ToLower(goal)
	gather := ""
	switch {
	case strings.Contains(low, "research"):
		gather = "research"
	case strings.Contains(low, "summarize each"), strings.Contains(low, "summarise each"):
		gather = "summarize"
	default:
		return nil
	}
	if !strings.Contains(low, "compar") && !strings.Contains(low, "write") {
		return nil
	}
	items := extractItems(goal)
	if len(items) < 2 {
		return nil
	}
	worker := "turn"
	specs := make([]StepSpec, 0, len(items)+1)
	deps := make([]int, 0, len(items))
	inputs := map[string]string{}
	for i, item := range items {
		specs = append(specs, StepSpec{
			Goal:     fmt.Sprintf("%s %s: cover what it is, its tradeoffs, and any notable facts.", capitalize(gather), item),
			Executor: worker,
			Post:     Postcondition{Kind: PostResultNonEmpty},
		})
		deps = append(deps, i)
		inputs[item] = fmt.Sprintf("#E%d", i)
	}
	specs = append(specs, StepSpec{
		Goal:     "Write a clear comparison of the items below, drawing only on the provided research.",
		Deps:     deps,
		Inputs:   inputs,
		Executor: "turn",
		Post:     Postcondition{Kind: PostResultNonEmpty},
	})
	return specs
}

// extractItems pulls the list of things to compare from a request, from the
// text after a "these"/"the following" marker or a colon, split on commas,
// conjunctions, and newlines.
func extractItems(goal string) []string {
	seg := goal
	for _, marker := range []string{"these", "the following", ":"} {
		if i := strings.Index(strings.ToLower(goal), marker); i >= 0 {
			seg = goal[i+len(marker):]
			break
		}
	}
	seg = strings.TrimRight(seg, ". ")
	var items []string
	for _, raw := range listSplitRE.Split(seg, -1) {
		it := strings.TrimSpace(strings.Trim(raw, "-*0123456789.: "))
		if it == "" || len(it) > 60 || strings.Contains(it, ".") {
			continue
		}
		if wc := len(strings.Fields(it)); wc == 0 || wc > 5 {
			continue
		}
		if containsAny(strings.ToLower(it), itemStop) {
			continue
		}
		items = append(items, it)
	}
	return items
}

const planSystem = `You are tomo's planner. Turn a job into the smallest plan that covers it.
Reply with ONLY a JSON array of steps, no prose. Each step is an object:
  "goal": one sentence describing what the step accomplishes
  "deps": array of earlier step indexes (0-based) this step needs; [] if none
  "inputs": object mapping a name to a literal or "#En" (the result of step n)
  "executor": "turn" for reasoning with tools, or "tool:<name>", or "worker:<name>"
  "postcondition": one of
     {"kind":"result_nonempty"}
     {"kind":"result_contains","text":"..."}
     {"kind":"file_exists","path":"..."}
     {"kind":"file_contains","path":"...","text":"..."}
     {"kind":"shell_zero","cmd":"..."}
Rules: prefer wide over deep and few substantial steps over many trivial ones.
A step's deps must reference only earlier (smaller) indexes. Prefer mechanical
postconditions over none. Most steps should be "turn".`

// planLLM asks the model for a plan in the StepSpec schema. feedback, when set,
// is a prior validation failure fed back so the model can correct it.
func (p *Planner) planLLM(ctx context.Context, goal, feedback string) ([]StepSpec, error) {
	user := "Job:\n" + goal
	if feedback != "" {
		user += "\n\nYour previous plan was invalid: " + feedback + "\nReturn a corrected plan."
	}
	req := provider.Request{
		Model:     p.Model,
		System:    planSystem,
		Messages:  []provider.Message{provider.UserText(user)},
		MaxTokens: p.MaxTokens,
	}
	resp, err := p.Provider.Stream(ctx, req, nil)
	if err != nil {
		return nil, err
	}
	var text strings.Builder
	for _, b := range resp.Blocks {
		if b.Type == provider.BlockText {
			text.WriteString(b.Text)
		}
	}
	specs, err := parsePlan(text.String())
	if err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("planner returned no steps")
	}
	return specs, nil
}

// parsePlan extracts the JSON array from a model reply that may be wrapped in
// prose or a code fence.
func parsePlan(s string) ([]StepSpec, error) {
	start := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON array in planner reply")
	}
	var specs []StepSpec
	if err := json.Unmarshal([]byte(s[start:end+1]), &specs); err != nil {
		return nil, fmt.Errorf("planner JSON: %w", err)
	}
	return specs, nil
}

// Validate checks a plan mechanically before it runs: every executor is known,
// every dependency and placeholder points strictly backward (which also makes
// the DAG acyclic), and every postcondition is one the orchestrator can
// evaluate. This is grounded plan-time validation, not asking a model if its
// plan is good.
func Validate(specs []StepSpec, tools, workers []string) error {
	if len(specs) == 0 {
		return fmt.Errorf("plan has no steps")
	}
	toolSet := set(tools)
	workerSet := set(workers)
	for i, s := range specs {
		if strings.TrimSpace(s.Goal) == "" {
			return fmt.Errorf("step %d has no goal", i)
		}
		if err := validExecutor(s.Executor, toolSet, workerSet); err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		for _, d := range s.Deps {
			if d < 0 || d >= i {
				return fmt.Errorf("step %d depends on %d, which is not an earlier step", i, d)
			}
		}
		for name, val := range s.Inputs {
			if m := placeholderRE.FindStringSubmatch(strings.TrimSpace(val)); m != nil {
				idx := atoi(m[1])
				if idx < 0 || idx >= i {
					return fmt.Errorf("step %d input %q references %s, not an earlier step", i, name, val)
				}
			}
		}
		if !s.Post.Evaluable() {
			return fmt.Errorf("step %d has an unevaluable postcondition %q", i, s.Post.Kind)
		}
	}
	return nil
}

func validExecutor(executor string, tools, workers map[string]bool) error {
	if executor == "turn" {
		return nil
	}
	if name, ok := strings.CutPrefix(executor, "tool:"); ok {
		if len(tools) == 0 || tools[name] {
			return nil
		}
		return fmt.Errorf("unknown tool %q", name)
	}
	if name, ok := strings.CutPrefix(executor, "worker:"); ok {
		if len(workers) == 0 || workers[name] {
			return nil
		}
		return fmt.Errorf("unknown worker %q", name)
	}
	return fmt.Errorf("unknown executor %q", executor)
}

func set(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}
