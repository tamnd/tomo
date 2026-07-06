// Package store persists sessions and their messages in one sqlite file, the
// ledger everything else replays from. Pure Go driver, so the binary stays
// CGO-free.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tamnd/tomo/pkg/provider"
)

// Store wraps the sqlite handle.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL UNIQUE,
	channel    TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
	id         INTEGER PRIMARY KEY,
	session_id INTEGER NOT NULL REFERENCES sessions(id),
	seq        INTEGER NOT NULL,
	role       TEXT NOT NULL,
	blocks     TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE (session_id, seq)
);
CREATE TABLE IF NOT EXISTS bindings (
	channel    TEXT NOT NULL,
	chat       TEXT NOT NULL,
	session    TEXT NOT NULL,
	PRIMARY KEY (channel, chat)
);
CREATE TABLE IF NOT EXISTS cron_jobs (
	id         INTEGER PRIMARY KEY,
	spec       TEXT NOT NULL,
	prompt     TEXT NOT NULL,
	channel    TEXT NOT NULL,
	chat       TEXT NOT NULL,
	enabled    INTEGER NOT NULL DEFAULT 1,
	last_run   TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	label      TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS cron_jobs_label ON cron_jobs(label) WHERE label != '';
CREATE TABLE IF NOT EXISTS cron_runs (
	id         INTEGER PRIMARY KEY,
	job_id     INTEGER NOT NULL,
	started_at TEXT NOT NULL,
	ok         INTEGER NOT NULL,
	output     TEXT NOT NULL
);
`

// Open opens (creating if needed) the ledger at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// One writer keeps sqlite locking out of the picture entirely.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// migrate brings an older ledger up to the current schema. Each step is safe to
// run again: a column that already exists is left alone.
func migrate(db *sql.DB) error {
	// label was added after cron_jobs first shipped.
	if _, err := db.Exec(`ALTER TABLE cron_jobs ADD COLUMN label TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS cron_jobs_label ON cron_jobs(label) WHERE label != ''`); err != nil {
		return err
	}
	return nil
}

// Close releases the handle.
func (s *Store) Close() error { return s.db.Close() }

// Session is one conversation's row.
type Session struct {
	ID       int64
	Name     string
	Channel  string
	Created  time.Time
	Updated  time.Time
	Messages int
}

// Session returns the session named name, creating it on first use.
func (s *Store) Session(name, channel string) (*Session, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.Exec(
		`INSERT INTO sessions (name, channel, created_at, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO NOTHING`, name, channel, now, now); err != nil {
		return nil, err
	}
	row := s.db.QueryRow(`SELECT id, name, channel, created_at, updated_at FROM sessions WHERE name = ?`, name)
	return scanSession(row)
}

// Sessions lists every session, most recently updated first.
func (s *Store) Sessions() ([]Session, error) {
	rows, err := s.db.Query(`
		SELECT s.id, s.name, s.channel, s.created_at, s.updated_at, COUNT(m.id)
		FROM sessions s LEFT JOIN messages m ON m.session_id = s.id
		GROUP BY s.id ORDER BY s.updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var sess Session
		var created, updated string
		if err := rows.Scan(&sess.ID, &sess.Name, &sess.Channel, &created, &updated, &sess.Messages); err != nil {
			return nil, err
		}
		sess.Created, _ = time.Parse(time.RFC3339, created)
		sess.Updated, _ = time.Parse(time.RFC3339, updated)
		out = append(out, sess)
	}
	return out, rows.Err()
}

// Append writes msgs to the session in order, after everything already there.
func (s *Store) Append(sessionID int64, msgs []provider.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM messages WHERE session_id = ?`, sessionID).Scan(&next); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i, m := range msgs {
		blocks, err := json.Marshal(m.Blocks)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO messages (session_id, seq, role, blocks, created_at) VALUES (?, ?, ?, ?, ?)`,
			sessionID, next+i, m.Role, string(blocks), now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, now, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// Messages replays a session's history in order.
func (s *Store) Messages(sessionID int64) ([]provider.Message, error) {
	rows, err := s.db.Query(`SELECT role, blocks FROM messages WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []provider.Message
	for rows.Next() {
		var role, blocks string
		if err := rows.Scan(&role, &blocks); err != nil {
			return nil, err
		}
		m := provider.Message{Role: provider.Role(role)}
		if err := json.Unmarshal([]byte(blocks), &m.Blocks); err != nil {
			return nil, fmt.Errorf("session %d: corrupt blocks: %w", sessionID, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Bind points a channel's chat at a named session, so the same conversation is
// reachable from more than one channel. Re-binding replaces the old target.
func (s *Store) Bind(channel, chat, session string) error {
	_, err := s.db.Exec(
		`INSERT INTO bindings (channel, chat, session) VALUES (?, ?, ?)
		 ON CONFLICT(channel, chat) DO UPDATE SET session = excluded.session`,
		channel, chat, session)
	return err
}

// BindingFor returns the session a chat is bound to, if any.
func (s *Store) BindingFor(channel, chat string) (string, bool, error) {
	var session string
	err := s.db.QueryRow(`SELECT session FROM bindings WHERE channel = ? AND chat = ?`, channel, chat).Scan(&session)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return session, true, nil
}

func scanSession(row *sql.Row) (*Session, error) {
	var sess Session
	var created, updated string
	if err := row.Scan(&sess.ID, &sess.Name, &sess.Channel, &created, &updated); err != nil {
		return nil, err
	}
	sess.Created, _ = time.Parse(time.RFC3339, created)
	sess.Updated, _ = time.Parse(time.RFC3339, updated)
	return &sess, nil
}
