package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Plan statuses. A plan is terminal once done, failed, or cancelled.
const (
	PlanPlanning  = "planning"
	PlanRunning   = "running"
	PlanPaused    = "paused"
	PlanDone      = "done"
	PlanFailed    = "failed"
	PlanCancelled = "cancelled"
)

// Step statuses, the lifecycle the orchestrator advances a step through. A step
// is terminal once done, failed, or skipped, and a terminal row is never
// rewritten: a retry is a fresh attempt row (see NewAttempt).
const (
	StepPending = "pending"
	StepReady   = "ready"
	StepRunning = "running"
	StepDone    = "done"
	StepFailed  = "failed"
	StepSkipped = "skipped"
)

// Plan is one job: a goal, a budget, and a status, owning a set of steps. It is
// the plan-as-artifact from spec 2080/ostres, written before anything runs.
type Plan struct {
	ID           string
	Session      string
	Channel      string
	Goal         string
	Status       string
	BudgetTokens int
	BudgetSteps  int
	BudgetWallMS int64
	SpentTokens  int
	Attended     bool
	Created      time.Time
	Updated      time.Time
}

// Step is one node of a plan's DAG. Deps and the #E<idx> placeholders in Inputs
// are indexes into the same plan, so the orchestrator schedules on Deps and
// resolves Inputs from earlier steps' results at dispatch time. A step may have
// several attempt rows; the ones the orchestrator drives are the latest per idx.
type Step struct {
	RowID     int64
	PlanID    string
	Idx       int
	Attempt   int
	UID       string
	Goal      string
	Deps      []int
	Inputs    map[string]string
	Executor  string
	PostJSON  string // the postcondition, opaque JSON the orch layer interprets
	Status    string
	Result    string
	Tokens    int
	StartedMS int64
	EndedMS   int64
}

// CreatePlan inserts a plan. An empty ID is filled with a fresh random id, which
// is returned so the caller can address the new plan.
func (s *Store) CreatePlan(p *Plan) (string, error) {
	if p.ID == "" {
		p.ID = randID()
	}
	now := time.Now().UTC()
	if p.Created.IsZero() {
		p.Created = now
	}
	p.Updated = now
	if p.Status == "" {
		p.Status = PlanPlanning
	}
	_, err := s.db.Exec(
		`INSERT INTO plans (id, session, channel, goal, status, budget_tokens, budget_steps, budget_wall, spent_tokens, attended, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Session, p.Channel, p.Goal, p.Status, p.BudgetTokens, p.BudgetSteps, p.BudgetWallMS,
		p.SpentTokens, boolInt(p.Attended), p.Created.UnixMilli(), p.Updated.UnixMilli())
	return p.ID, err
}

// AddStep appends a step to a plan as its first attempt. It fills the UID if
// empty and returns the new row id.
func (s *Store) AddStep(st *Step) (int64, error) {
	if st.UID == "" {
		st.UID = randID()
	}
	if st.Status == "" {
		st.Status = StepPending
	}
	deps, _ := json.Marshal(st.Deps)
	inputs, _ := json.Marshal(st.Inputs)
	post := st.PostJSON
	if post == "" {
		post = "{}"
	}
	res, err := s.db.Exec(
		`INSERT INTO plan_steps (plan_id, idx, attempt, uid, goal, deps, inputs, executor, postcondition, status, result, tokens, started_at, ended_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		st.PlanID, st.Idx, st.Attempt, st.UID, st.Goal, string(deps), string(inputs), st.Executor, post,
		st.Status, st.Result, st.Tokens, st.StartedMS, st.EndedMS)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	st.RowID = id
	return id, err
}

// Plan reads one plan by id.
func (s *Store) Plan(id string) (*Plan, error) {
	row := s.db.QueryRow(
		`SELECT id, session, channel, goal, status, budget_tokens, budget_steps, budget_wall, spent_tokens, attended, created_at, updated_at
		 FROM plans WHERE id = ?`, id)
	return scanPlan(row)
}

// Plans lists plans, newest first, optionally filtered to a status.
func (s *Store) Plans(status string) ([]Plan, error) {
	q := `SELECT id, session, channel, goal, status, budget_tokens, budget_steps, budget_wall, spent_tokens, attended, created_at, updated_at FROM plans`
	args := []any{}
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Plan
	for rows.Next() {
		p, err := scanPlanRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// SetPlanStatus flips a plan's status and touches updated_at.
func (s *Store) SetPlanStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE plans SET status = ?, updated_at = ? WHERE id = ?`,
		status, time.Now().UTC().UnixMilli(), id)
	return err
}

// AddSpent adds to a plan's running token total and returns the new total.
func (s *Store) AddSpent(id string, tokens int) (int, error) {
	if _, err := s.db.Exec(`UPDATE plans SET spent_tokens = spent_tokens + ?, updated_at = ? WHERE id = ?`,
		tokens, time.Now().UTC().UnixMilli(), id); err != nil {
		return 0, err
	}
	var total int
	err := s.db.QueryRow(`SELECT spent_tokens FROM plans WHERE id = ?`, id).Scan(&total)
	return total, err
}

// Steps returns the latest attempt of every step in a plan, ordered by idx, the
// live view the orchestrator schedules over.
func (s *Store) Steps(planID string) ([]Step, error) {
	rows, err := s.db.Query(
		`SELECT rowid, plan_id, idx, attempt, uid, goal, deps, inputs, executor, postcondition, status, result, tokens, started_at, ended_at
		 FROM plan_steps WHERE plan_id = ? AND (idx, attempt) IN
		   (SELECT idx, MAX(attempt) FROM plan_steps WHERE plan_id = ? GROUP BY idx)
		 ORDER BY idx`, planID, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSteps(rows)
}

// StepHistory returns every attempt row for a plan, oldest first, the full
// append-only journal an audit or a `tomo plan` view reads.
func (s *Store) StepHistory(planID string) ([]Step, error) {
	rows, err := s.db.Query(
		`SELECT rowid, plan_id, idx, attempt, uid, goal, deps, inputs, executor, postcondition, status, result, tokens, started_at, ended_at
		 FROM plan_steps WHERE plan_id = ? ORDER BY idx, attempt`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSteps(rows)
}

// MarkStep advances a step row's status, and when terminal records its result,
// tokens, and timestamps. It refuses to rewrite a row that is already terminal,
// which is the append-only discipline: a retry must be a NewAttempt, not an
// overwrite of a done or failed row.
func (s *Store) MarkStep(rowID int64, status, result string, tokens int, startedMS, endedMS int64) error {
	var cur string
	if err := s.db.QueryRow(`SELECT status FROM plan_steps WHERE rowid = ?`, rowID).Scan(&cur); err != nil {
		return err
	}
	if isTerminalStep(cur) {
		return fmt.Errorf("step %d is terminal (%s); a retry must be a new attempt", rowID, cur)
	}
	_, err := s.db.Exec(
		`UPDATE plan_steps SET status = ?, result = ?, tokens = ?, started_at = ?, ended_at = ? WHERE rowid = ?`,
		status, result, tokens, startedMS, endedMS, rowID)
	return err
}

// NewAttempt clones a step's definition into a fresh pending row with the next
// attempt number, leaving the failed attempt in place. This is how repair works
// without destroying the history that makes replay and audit honest.
func (s *Store) NewAttempt(from Step) (int64, error) {
	next := Step{
		PlanID:   from.PlanID,
		Idx:      from.Idx,
		Attempt:  from.Attempt + 1,
		Goal:     from.Goal,
		Deps:     from.Deps,
		Inputs:   from.Inputs,
		Executor: from.Executor,
		PostJSON: from.PostJSON,
		Status:   StepPending,
	}
	return s.AddStep(&next)
}

func isTerminalStep(status string) bool {
	return status == StepDone || status == StepFailed || status == StepSkipped
}

func scanPlan(row *sql.Row) (*Plan, error) {
	var p Plan
	var attended int
	var created, updated int64
	if err := row.Scan(&p.ID, &p.Session, &p.Channel, &p.Goal, &p.Status, &p.BudgetTokens, &p.BudgetSteps,
		&p.BudgetWallMS, &p.SpentTokens, &attended, &created, &updated); err != nil {
		return nil, err
	}
	p.Attended = attended != 0
	p.Created = time.UnixMilli(created).UTC()
	p.Updated = time.UnixMilli(updated).UTC()
	return &p, nil
}

func scanPlanRows(rows *sql.Rows) (*Plan, error) {
	var p Plan
	var attended int
	var created, updated int64
	if err := rows.Scan(&p.ID, &p.Session, &p.Channel, &p.Goal, &p.Status, &p.BudgetTokens, &p.BudgetSteps,
		&p.BudgetWallMS, &p.SpentTokens, &attended, &created, &updated); err != nil {
		return nil, err
	}
	p.Attended = attended != 0
	p.Created = time.UnixMilli(created).UTC()
	p.Updated = time.UnixMilli(updated).UTC()
	return &p, nil
}

func scanSteps(rows *sql.Rows) ([]Step, error) {
	var out []Step
	for rows.Next() {
		var st Step
		var deps, inputs string
		if err := rows.Scan(&st.RowID, &st.PlanID, &st.Idx, &st.Attempt, &st.UID, &st.Goal, &deps, &inputs,
			&st.Executor, &st.PostJSON, &st.Status, &st.Result, &st.Tokens, &st.StartedMS, &st.EndedMS); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(deps), &st.Deps)
		_ = json.Unmarshal([]byte(inputs), &st.Inputs)
		out = append(out, st)
	}
	return out, rows.Err()
}

// randID returns a short random hex id, enough entropy for plan and step ids
// without pulling in a uuid dependency.
func randID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
