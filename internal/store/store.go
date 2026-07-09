// Package store persists jobs, per-chat settings, and per-(chat,project)
// session IDs (for --resume) in SQLite.
package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/Fosterist/claude-anywhere/internal/api"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id INTEGER NOT NULL,
		project TEXT NOT NULL,
		prompt TEXT NOT NULL,
		session_id TEXT,
		permission TEXT NOT NULL,
		max_budget_usd REAL,
		status TEXT NOT NULL DEFAULT 'pending',
		result TEXT,
		cost_usd REAL,
		error_text TEXT,
		created_at INTEGER NOT NULL,
		completed_at INTEGER
	);
	CREATE TABLE IF NOT EXISTS sessions (
		chat_id INTEGER NOT NULL,
		project TEXT NOT NULL,
		session_id TEXT NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (chat_id, project)
	);
	CREATE TABLE IF NOT EXISTS chat_state (
		chat_id INTEGER PRIMARY KEY,
		current_project TEXT,
		mode TEXT NOT NULL DEFAULT 'auto',
		offline_behavior TEXT NOT NULL DEFAULT 'queue'
	);
	`)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Enqueue adds a new pending job, filling in the last known session ID for
// this (chat, project) pair so the agent can --resume automatically.
func (s *Store) Enqueue(chatID int64, project, prompt, permission string, maxBudget float64) (int64, error) {
	sessionID, _ := s.LastSession(chatID, project)
	res, err := s.db.Exec(
		`INSERT INTO jobs (chat_id, project, prompt, session_id, permission, max_budget_usd, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'pending', ?)`,
		chatID, project, prompt, sessionID, permission, maxBudget, time.Now().Unix(),
	)
	if err != nil {
		return 0, fmt.Errorf("enqueue: %w", err)
	}
	return res.LastInsertId()
}

// ClaimNext atomically picks the oldest pending job and marks it running.
// Returns nil, nil if the queue is empty.
func (s *Store) ClaimNext() (*api.Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	row := tx.QueryRow(`
		SELECT id, project, prompt, session_id, permission, max_budget_usd
		FROM jobs WHERE status = 'pending' ORDER BY id LIMIT 1`)

	var j api.Job
	var sessionID sql.NullString
	var maxBudget sql.NullFloat64
	if err := row.Scan(&j.ID, &j.Project, &j.Prompt, &sessionID, &j.Permission, &maxBudget); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("claim: %w", err)
	}
	j.SessionID = sessionID.String
	j.MaxBudget = maxBudget.Float64

	if _, err := tx.Exec(`UPDATE jobs SET status = 'running' WHERE id = ?`, j.ID); err != nil {
		return nil, fmt.Errorf("mark running: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &j, nil
}

// Complete records the agent's result and remembers the session ID for
// the next prompt against the same (chat, project) pair. Returns the
// chat ID the job belonged to, so the caller can notify that chat.
func (s *Store) Complete(res api.Result) (chatID int64, err error) {
	status := "done"
	if res.IsError {
		status = "error"
	}
	if err = s.db.QueryRow(`SELECT chat_id FROM jobs WHERE id = ?`, res.JobID).Scan(&chatID); err != nil {
		return 0, fmt.Errorf("lookup chat for job %d: %w", res.JobID, err)
	}

	_, err = s.db.Exec(
		`UPDATE jobs SET status = ?, result = ?, cost_usd = ?, error_text = ?, completed_at = ?
		 WHERE id = ?`,
		status, res.Result, res.CostUSD, res.ErrorText, time.Now().Unix(), res.JobID,
	)
	if err != nil {
		return chatID, fmt.Errorf("complete: %w", err)
	}
	if res.SessionID != "" {
		var project string
		if err := s.db.QueryRow(`SELECT project FROM jobs WHERE id = ?`, res.JobID).Scan(&project); err == nil {
			s.db.Exec(`
				INSERT INTO sessions (chat_id, project, session_id, updated_at) VALUES (?, ?, ?, ?)
				ON CONFLICT(chat_id, project) DO UPDATE SET session_id = excluded.session_id, updated_at = excluded.updated_at`,
				chatID, project, res.SessionID, time.Now().Unix())
		}
	}
	return chatID, nil
}

func (s *Store) LastSession(chatID int64, project string) (string, error) {
	var sessionID string
	err := s.db.QueryRow(`SELECT session_id FROM sessions WHERE chat_id = ? AND project = ?`, chatID, project).Scan(&sessionID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return sessionID, err
}

// RecentCost sums cost_usd for jobs completed within the given window —
// the empirical stand-in for a real rate-limit quota.
func (s *Store) RecentCost(chatID int64, window time.Duration) (totalUSD float64, count int, err error) {
	since := time.Now().Add(-window).Unix()
	row := s.db.QueryRow(
		`SELECT COALESCE(SUM(cost_usd), 0), COUNT(*) FROM jobs
		 WHERE chat_id = ? AND completed_at >= ? AND status = 'done'`,
		chatID, since,
	)
	err = row.Scan(&totalUSD, &count)
	return
}

type ChatState struct {
	CurrentProject   string
	Mode             string // "auto" | "confirm"
	OfflineBehavior  string // "queue" | "notify"
}

func (s *Store) GetChatState(chatID int64) (ChatState, error) {
	var st ChatState
	err := s.db.QueryRow(
		`SELECT current_project, mode, offline_behavior FROM chat_state WHERE chat_id = ?`, chatID,
	).Scan(&st.CurrentProject, &st.Mode, &st.OfflineBehavior)
	if err == sql.ErrNoRows {
		return ChatState{Mode: "auto", OfflineBehavior: "queue"}, nil
	}
	return st, err
}

func (s *Store) SetProject(chatID int64, project string) error {
	_, err := s.db.Exec(`
		INSERT INTO chat_state (chat_id, current_project) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET current_project = excluded.current_project`,
		chatID, project)
	return err
}

func (s *Store) SetMode(chatID int64, mode string) error {
	_, err := s.db.Exec(`
		INSERT INTO chat_state (chat_id, mode) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET mode = excluded.mode`,
		chatID, mode)
	return err
}

func (s *Store) SetOfflineBehavior(chatID int64, behavior string) error {
	_, err := s.db.Exec(`
		INSERT INTO chat_state (chat_id, offline_behavior) VALUES (?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET offline_behavior = excluded.offline_behavior`,
		chatID, behavior)
	return err
}

func (s *Store) Close() error { return s.db.Close() }
