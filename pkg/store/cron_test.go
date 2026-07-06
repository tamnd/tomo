package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openCronStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "cron.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestJobLifecycle(t *testing.T) {
	st := openCronStore(t)

	id, err := st.AddJob("@every 5m", "poll", "web", "c1")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := st.Jobs()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %v err %v", jobs, err)
	}
	j := jobs[0]
	if j.ID != id || j.Spec != "@every 5m" || j.Prompt != "poll" || !j.Enabled || j.Label != "" {
		t.Fatalf("job = %+v", j)
	}

	if err := st.SetJobEnabled(id, false); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := st.EnabledJobs(); len(enabled) != 0 {
		t.Errorf("disabled job still enabled: %v", enabled)
	}

	when := time.Now().Add(-time.Minute)
	if err := st.RecordRun(id, when, true, "did it"); err != nil {
		t.Fatal(err)
	}
	runs, err := st.Runs(10)
	if err != nil || len(runs) != 1 || runs[0].JobID != id || !runs[0].OK {
		t.Fatalf("runs = %+v err %v", runs, err)
	}

	ok, err := st.RemoveJob(id)
	if err != nil || !ok {
		t.Fatalf("remove = %v err %v", ok, err)
	}
	if ok, _ := st.RemoveJob(id); ok {
		t.Error("second remove should report missing")
	}
}

func TestEnsureJobIsIdempotent(t *testing.T) {
	st := openCronStore(t)

	id, err := st.EnsureJob("heartbeat", "@every 30m", "check things", "web", "c1")
	if err != nil {
		t.Fatal(err)
	}
	// It ran once so we have a last_run to preserve across the next ensure.
	if err := st.MarkRun(id, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Re-ensuring with a new spec updates in place, keeps the id and last_run.
	id2, err := st.EnsureJob("heartbeat", "@every 15m", "check things harder", "telegram", "c9")
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id {
		t.Fatalf("ensure made a new job %d, want %d", id2, id)
	}
	jobs, _ := st.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("ensure duplicated the job: %+v", jobs)
	}
	j := jobs[0]
	if j.Spec != "@every 15m" || j.Prompt != "check things harder" || j.Channel != "telegram" || j.Chat != "c9" {
		t.Errorf("ensure did not refresh fields: %+v", j)
	}
	if j.LastRun.IsZero() {
		t.Error("ensure reset last_run, breaking catch-up")
	}
}
