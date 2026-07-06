package schedule

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tamnd/tomo/pkg/channel"
	"github.com/tamnd/tomo/pkg/store"
)

// recordPoster captures what the scheduler pushed.
type recordPoster struct {
	mu   sync.Mutex
	sent []string
}

func (p *recordPoster) Post(_ context.Context, _, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, text)
	return nil
}

func (p *recordPoster) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.sent...)
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestDueJobRunsAndPosts(t *testing.T) {
	poster := &recordPoster{}
	var ran int
	run := func(_ context.Context, _, _, prompt string) (string, error) {
		ran++
		return "result for " + prompt, nil
	}
	st := openStore(t)

	id, err := st.AddJob("@every 1m", "check mail", "web", "c1")
	if err != nil {
		t.Fatal(err)
	}
	// Last ran two minutes ago, so it is already due.
	if err := st.MarkRun(id, time.Now().Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	s := New(st, run, map[string]channel.Poster{"web": poster})
	s.checkDue(context.Background())

	if ran != 1 {
		t.Fatalf("job ran %d times, want 1", ran)
	}
	if got := poster.all(); len(got) != 1 || got[0] != "result for check mail" {
		t.Errorf("posted = %v", got)
	}
	runs, err := st.Runs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || !runs[0].OK {
		t.Errorf("runs = %+v", runs)
	}
}

func TestNotDueJobIsSkipped(t *testing.T) {
	var ran int
	run := func(context.Context, string, string, string) (string, error) { ran++; return "x", nil }
	st := openStore(t)

	id, _ := st.AddJob("0 0 * * *", "daily thing", "web", "c1")
	// Ran an hour ago; the next midnight is far off, so nothing is due.
	_ = st.MarkRun(id, time.Now().Add(-time.Hour))

	New(st, run, nil).checkDue(context.Background())
	if ran != 0 {
		t.Errorf("job ran %d times, want 0", ran)
	}
}

func TestMissedTicksCollapseToOneRun(t *testing.T) {
	var ran int
	run := func(context.Context, string, string, string) (string, error) { ran++; return "", nil }
	st := openStore(t)

	id, _ := st.AddJob("@every 1m", "frequent", "web", "c1")
	// The process was down for an hour; catch-up must run exactly once.
	_ = st.MarkRun(id, time.Now().Add(-time.Hour))

	s := New(st, run, nil)
	s.checkDue(context.Background())
	s.checkDue(context.Background()) // a second immediate pass must not re-fire
	if ran != 1 {
		t.Errorf("catch-up ran %d times, want 1", ran)
	}
}

func TestEmptyOutputStaysQuiet(t *testing.T) {
	poster := &recordPoster{}
	run := func(context.Context, string, string, string) (string, error) { return "", nil }
	st := openStore(t)
	id, _ := st.AddJob("@every 1m", "heartbeat", "web", "c1")
	_ = st.MarkRun(id, time.Now().Add(-2*time.Minute))

	New(st, run, map[string]channel.Poster{"web": poster}).checkDue(context.Background())
	if got := poster.all(); len(got) != 0 {
		t.Errorf("empty output should post nothing, got %v", got)
	}
	// But it still recorded the run.
	runs, _ := st.Runs(10)
	if len(runs) != 1 {
		t.Errorf("runs = %+v", runs)
	}
}
