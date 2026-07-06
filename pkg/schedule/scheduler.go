package schedule

import (
	"context"
	"time"

	"github.com/tamnd/tomo/pkg/store"
)

// RunFunc executes a job's prompt as a background turn and returns the
// assistant's text. The router supplies it.
type RunFunc func(ctx context.Context, channel, chat, prompt string) (string, error)

// Poster pushes a message to a chat on its own, outside a reply. The scheduler
// needs it to deliver background results. A front door that can only answer
// when spoken to (the web chat, which has no durable client to push to) simply
// does not provide one, and its jobs run and record but deliver nothing.
type Poster interface {
	Post(ctx context.Context, chat, text string) error
}

// Scheduler ticks once a minute, runs the jobs that have come due, records each
// run, and delivers any output to the job's channel.
type Scheduler struct {
	store   *store.Store
	run     RunFunc
	posters map[string]Poster
	now     func() time.Time
	tick    time.Duration
}

// New builds a scheduler. posters maps a channel name to the thing that can
// push a message there; a job whose channel has no poster still runs and is
// recorded, its output just is not delivered.
func New(st *store.Store, run RunFunc, posters map[string]Poster) *Scheduler {
	return &Scheduler{store: st, run: run, posters: posters, now: time.Now, tick: time.Minute}
}

// Run checks due jobs immediately, then every minute, until ctx is cancelled.
// The startup check is the missed-tick catch-up: a job that came due while the
// process was down runs once now, no matter how many ticks it missed.
func (s *Scheduler) Run(ctx context.Context) error {
	s.checkDue(ctx)
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			s.checkDue(ctx)
		}
	}
}

func (s *Scheduler) checkDue(ctx context.Context) {
	now := s.now()
	jobs, err := s.store.EnabledJobs()
	if err != nil {
		return
	}
	for _, job := range jobs {
		sched, err := Parse(job.Spec)
		if err != nil {
			continue // a malformed spec should not stall the loop
		}
		from := job.LastRun
		if from.IsZero() {
			from = job.Created
		}
		if next := sched.Next(from); next.IsZero() || next.After(now) {
			continue
		}
		// Mark before running so a slow job does not fire twice, and so many
		// missed ticks collapse into this single catch-up run.
		_ = s.store.MarkRun(job.ID, now)
		s.runJob(ctx, job, now)
	}
}

func (s *Scheduler) runJob(ctx context.Context, job store.Job, when time.Time) {
	out, err := s.run(ctx, job.Channel, job.Chat, job.Prompt)
	if err != nil {
		_ = s.store.RecordRun(job.ID, when, false, err.Error())
		return
	}
	_ = s.store.RecordRun(job.ID, when, true, out)
	// Stay quiet when there is nothing to say.
	if out == "" {
		return
	}
	if p := s.posters[job.Channel]; p != nil {
		_ = p.Post(ctx, job.Chat, out)
	}
}
