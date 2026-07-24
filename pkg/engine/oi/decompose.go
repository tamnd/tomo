package oi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tamnd/tomo/pkg/agent"
	"github.com/tamnd/tomo/pkg/provider"
)

// The checklist decomposer (spec 2109, the lever after the test-authoring sub-flow)
// closes the gap that testgen left open. testgen authors a reproduction for the
// issue and holds the finish to a red-to-green against it, which fixed targeting:
// on a single-behavior bug the model localizes to the right subsystem and turns the
// authored test green. But an issue is often not one behavior. Measured on
// dynaconf-1225 (experiment 0081): the issue is titled "Ports from #1204 to master"
// and is a thirteen-item checklist, and testgen faithfully authored one test per
// item. Handed the whole checklist as a single red wall, a free model became a
// thirteen-feature porter: it edited a dozen files at once, broke test collection so
// even the one graded item went uncollectable, and never turned its own broad
// reproduction green, so it never converged and churned to the round cap. The wall
// was not targeting and not minimality, it was decomposition: nothing made the model
// land one item at a time.
//
// The decomposer does. Before the loop it makes one focused call over the ISSUE TEXT
// ALONE and asks whether the issue is several independent items, and if so to return
// them ordered smallest-and-most-foundational first. A single-item issue disarms the
// decomposer and the run falls back to the whole-issue sub-flow, so nothing is lost
// on the bugs testgen already handled. On a real checklist it authors a reproduction
// for the FIRST item only, writes it to the same scratch path, and arms the
// reproduction gate on it, so the model's whole job is to turn one small test green.
// When that item lands (its reproduction goes red-to-green and the regression guard
// confirms nothing that passed before now fails), the decomposer folds the item's
// fix into the protected baseline, authors the next item's reproduction over the
// same path, and re-arms the gate. The model walks the checklist one coherent slice
// at a time, keeping the baseline green between items, instead of climbing one wall
// it cannot climb.
//
// It stays on the right side of the no-tailoring line the same way the other
// sub-flows do. The split call and every per-item authoring call read only the issue
// the task shipped with, never the workspace's own tests and never the hidden grading
// suite, and name no file or symbol the harness supplied. The items are the issue
// author's own checklist turned into an order of work; the tests are their concrete
// cases turned executable. Armed opt-in (TOMO_OI_DECOMPOSE=1) so it can be A/B'd, and
// it supersedes the test-authoring sub-flow rather than stacking with it, so a
// checklist run authors one item at a time and a single-item run pays exactly the
// testgen cost plus one split call.

// maxItems caps how many checklist items the decomposer walks. An issue that splits
// into more than this is either genuinely enormous, where landing the first several
// items in order is still the right move and the tail is out of a cheap model's
// reach anyway, or the split over-fragmented, where the cap keeps the walk bounded.
// Either way the run authors and lands items in order up to the cap rather than
// spending the split into an unbounded sequence.
const maxItems = 8

// splitSystem is the whole instruction for the split call. It asks the model to read
// the issue and decide whether it describes several independent pieces of work, and
// if so to return them as an ordered JSON array of short imperative descriptions,
// most-foundational and smallest first, so the walk lands the base pieces before the
// ones that build on them. It asks for a single-element array when the issue is one
// coherent change, which is the signal to fall back, and forbids inventing work the
// issue does not state so the items stay the issue author's own checklist.
const splitSystem = "You read a bug report or feature request and decide whether it describes SEVERAL independent pieces of work or ONE coherent change. " +
	"Many issues, especially ports and 'implement the following' checklists, bundle multiple unrelated changes; others are a single fix. " +
	"If the issue is several independent items, return them as a JSON array of short imperative strings, one per item, ordered so the most foundational and smallest items come FIRST and items that build on earlier ones come later. " +
	"If the issue is one coherent change, return a JSON array with that single change as its one element. " +
	"Do not invent work the issue does not state, do not split one change into artificial steps, and do not merge distinct items. " +
	"Output ONLY the JSON array, nothing before or after it."

// itemPrompt frames one checklist item as the authoring target while keeping the full
// issue as context, so the per-item reproduction covers exactly that item's concrete
// cases and not the whole checklist. The item text leads, since it is what the test
// must reproduce, and the issue follows as the ground truth the item was drawn from.
const itemPrompt = "Write a reproduction test for ONLY this one item of a larger issue:\n\n%[1]s\n\n" +
	"Cover only this item's concrete behavior. The other items of the issue are being handled separately, so do not test them. " +
	"Here is the full issue for context, but your test must target only the item above:\n\n%[2]s"

// decomposeInitial is injected when the first item's reproduction is installed. It
// tells the model the issue is a checklist being worked one item at a time, names how
// many items there are and which one is active, and points it at the scratch test to
// turn green, the same red-to-green contract testgen sets but scoped to one item.
const decomposeInitial = "This issue is a checklist of %[1]d separate items. You will work them ONE AT A TIME, smallest and most foundational first, so the code stays working between items instead of changing everything at once. " +
	"Item 1 of %[1]d is:\n\n%[2]s\n\n" +
	"A reproduction test for this item alone has been written to ./%[3]s and it currently FAILS. " +
	"Run it (`python -m pytest %[3]s -rA`), then edit the PROJECT SOURCE until it passes, without editing or weakening ./%[3]s. " +
	"Fix only this item for now. When it is green you will be given the next item; do not jump ahead to later items."

// decomposeNext is injected when an item lands and the next item's reproduction is
// installed. It confirms the just-finished item, names the new active item, and
// points at the refreshed scratch test, telling the model to keep the earlier items'
// fixes intact while it makes the new one green.
const decomposeNext = "Item %[1]d of %[2]d is done: its reproduction is green. " +
	"Now item %[3]d of %[2]d:\n\n%[4]s\n\n" +
	"The reproduction test ./%[5]s has been REPLACED with one for this new item, and it currently FAILS. " +
	"Run it (`python -m pytest %[5]s -rA`), then edit the PROJECT SOURCE until it passes, without editing ./%[5]s and without breaking the items you already fixed. " +
	"Fix only this item for now."

// decomposer holds the state of one checklist walk: the ordered item descriptions and
// the index of the item currently being worked. It is created once per turn when the
// decomposer is armed and the issue splits into more than one item; a nil or
// single-item decomposer is inert and the turn behaves as if the lever were off.
type decomposer struct {
	e     *Engine
	items []string
	idx   int // index of the item currently being worked
}

// armed reports whether the decomposer is driving a real checklist walk, which is
// true only when it exists and holds more than one item. A single-item split is not a
// checklist and leaves the walk inert so the whole-issue sub-flow runs instead.
func (d *decomposer) armed() bool {
	return d != nil && len(d.items) > 1
}

// begin runs the split call, and if the issue is a checklist, authors and installs the
// first item's reproduction and returns the initial directive plus true. It returns
// ("", false) when the decomposer is off, there is no workspace, the issue does not
// split into more than one item, or the first item's reproduction will not collect, so
// a caller that gets false falls back to the whole-issue test-authoring sub-flow. On
// success the decomposer is left holding the ordered items at index zero.
func (d *decomposer) begin(ctx context.Context, issue string, sink agent.Sink) (string, bool) {
	issue = strings.TrimSpace(issue)
	if issue == "" || d.e.Workspace == "" {
		return "", false
	}
	items := d.e.splitIssue(ctx, issue)
	if len(items) <= 1 {
		return "", false
	}
	if len(items) > maxItems {
		items = items[:maxItems]
	}
	d.items = items
	d.idx = 0
	if !d.e.authorAndInstall(ctx, fmt.Sprintf(itemPrompt, items[0], issue)) {
		// The first item's reproduction will not collect. Rather than arm a checklist
		// walk on a broken target, disarm and let the whole-issue sub-flow try instead.
		d.items = nil
		return "", false
	}
	if sink != nil {
		sink.Text(fmt.Sprintf("\n[decompose] %d items, working 1/%d\n", len(items), len(items)))
	}
	return fmt.Sprintf(decomposeInitial, len(items), items[0], reproTestFile), true
}

// advance moves to the next item after the current one has landed. It authors and
// installs the next item's reproduction over the same scratch path and returns the
// directive plus true, skipping any item whose reproduction will not collect so a
// single bad target does not stall the walk. It returns ("", false) when the items are
// exhausted, which is the signal to finish the turn. It must be called only at a
// finish where the current item's reproduction has gone green, so the item it leaves
// behind is genuinely done.
func (d *decomposer) advance(ctx context.Context, issue string, sink agent.Sink) (string, bool) {
	done := d.idx
	for d.idx++; d.idx < len(d.items); d.idx++ {
		if d.e.authorAndInstall(ctx, fmt.Sprintf(itemPrompt, d.items[d.idx], issue)) {
			if sink != nil {
				sink.Text(fmt.Sprintf("\n[decompose] item %d/%d done, working %d/%d\n", done+1, len(d.items), d.idx+1, len(d.items)))
			}
			return fmt.Sprintf(decomposeNext, done+1, len(d.items), d.idx+1, d.items[d.idx], reproTestFile), true
		}
	}
	if sink != nil {
		sink.Text(fmt.Sprintf("\n[decompose] item %d/%d done, all items complete\n", done+1, len(d.items)))
	}
	return "", false
}

// splitIssue makes the split call and returns the issue's items in work order, or a
// nil slice on any failure so the caller treats it as a non-checklist. It reads only
// the issue text, parses the model's JSON array, and drops blank entries; a reply that
// is not a clean array, or that carries no usable item, yields nil rather than a guess.
func (e *Engine) splitIssue(ctx context.Context, issue string) []string {
	resp, err := e.stream(ctx, provider.Request{
		Model:    e.Model,
		System:   splitSystem,
		Messages: []provider.Message{provider.UserText(issue)},
	}, nil)
	if err != nil || resp == nil {
		return nil
	}
	return parseItems(assistantText(resp.Blocks))
}

// parseItems lifts the JSON array of item strings out of the split reply. It finds the
// first bracketed array in the text, tolerating prose or a code fence around it, and
// unmarshals it into trimmed non-empty strings. A reply with no array, or an array
// that does not decode to strings, returns nil, the non-checklist signal.
func parseItems(reply string) []string {
	start := strings.IndexByte(reply, '[')
	end := strings.LastIndexByte(reply, ']')
	if start < 0 || end <= start {
		return nil
	}
	var raw []string
	if err := json.Unmarshal([]byte(reply[start:end+1]), &raw); err != nil {
		return nil
	}
	var items []string
	for _, s := range raw {
		if s = strings.TrimSpace(s); s != "" {
			items = append(items, s)
		}
	}
	return items
}
