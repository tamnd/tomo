package store

import (
	"database/sql"
	"time"
)

// Job is one scheduled prompt. LastRun is zero until it has run once. Label is
// empty for user jobs; a non-empty label marks a job the daemon owns and keeps
// unique, like the heartbeat.
type Job struct {
	ID      int64
	Spec    string
	Prompt  string
	Channel string
	Chat    string
	Enabled bool
	LastRun time.Time
	Created time.Time
	Label   string
}

// Run is one recorded execution of a job.
type Run struct {
	ID      int64
	JobID   int64
	Started time.Time
	OK      bool
	Output  string
}

// AddJob inserts an enabled job and returns its id.
func (s *Store) AddJob(spec, prompt, channel, chat string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(
		`INSERT INTO cron_jobs (spec, prompt, channel, chat, enabled, created_at)
		 VALUES (?, ?, ?, ?, 1, ?)`, spec, prompt, channel, chat, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EnsureJob upserts a labelled job the daemon owns, keyed on label. On first
// call it inserts and returns the new id; later it refreshes the spec, prompt,
// channel, and chat so config changes take effect without duplicating the job.
// It never resets last_run, so catch-up still works across restarts.
func (s *Store) EnsureJob(label, spec, prompt, channel, chat string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(
		`INSERT INTO cron_jobs (spec, prompt, channel, chat, enabled, created_at, label)
		 VALUES (?, ?, ?, ?, 1, ?, ?)
		 ON CONFLICT(label) WHERE label != '' DO UPDATE SET
		   spec = excluded.spec, prompt = excluded.prompt,
		   channel = excluded.channel, chat = excluded.chat`,
		spec, prompt, channel, chat, now, label)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(`SELECT id FROM cron_jobs WHERE label = ?`, label).Scan(&id)
	return id, err
}

// Jobs lists every job, oldest first.
func (s *Store) Jobs() ([]Job, error) {
	rows, err := s.db.Query(`SELECT id, spec, prompt, channel, chat, enabled, last_run, created_at, label FROM cron_jobs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

// EnabledJobs lists only the jobs the scheduler should consider.
func (s *Store) EnabledJobs() ([]Job, error) {
	rows, err := s.db.Query(`SELECT id, spec, prompt, channel, chat, enabled, last_run, created_at, label FROM cron_jobs WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanJobs(rows)
}

// RemoveJob deletes a job and reports whether it existed.
func (s *Store) RemoveJob(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM cron_jobs WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetJobEnabled toggles a job on or off.
func (s *Store) SetJobEnabled(id int64, enabled bool) error {
	_, err := s.db.Exec(`UPDATE cron_jobs SET enabled = ? WHERE id = ?`, boolInt(enabled), id)
	return err
}

// MarkRun records that a job ran at the given time, so the next due time is
// computed from here. Setting it to now collapses any missed ticks into one.
func (s *Store) MarkRun(id int64, when time.Time) error {
	_, err := s.db.Exec(`UPDATE cron_jobs SET last_run = ? WHERE id = ?`, when.UTC().Format(time.RFC3339), id)
	return err
}

// RecordRun appends a run to the history.
func (s *Store) RecordRun(jobID int64, when time.Time, ok bool, output string) error {
	_, err := s.db.Exec(
		`INSERT INTO cron_runs (job_id, started_at, ok, output) VALUES (?, ?, ?, ?)`,
		jobID, when.UTC().Format(time.RFC3339), boolInt(ok), output)
	return err
}

// Runs returns the most recent runs across all jobs, newest first.
func (s *Store) Runs(limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id, job_id, started_at, ok, output FROM cron_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var started string
		var ok int
		if err := rows.Scan(&r.ID, &r.JobID, &started, &ok, &r.Output); err != nil {
			return nil, err
		}
		r.Started, _ = time.Parse(time.RFC3339, started)
		r.OK = ok != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanJobs(rows *sql.Rows) ([]Job, error) {
	var out []Job
	for rows.Next() {
		var j Job
		var enabled int
		var lastRun, created string
		if err := rows.Scan(&j.ID, &j.Spec, &j.Prompt, &j.Channel, &j.Chat, &enabled, &lastRun, &created, &j.Label); err != nil {
			return nil, err
		}
		j.Enabled = enabled != 0
		if lastRun != "" {
			j.LastRun, _ = time.Parse(time.RFC3339, lastRun)
		}
		j.Created, _ = time.Parse(time.RFC3339, created)
		out = append(out, j)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
