package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/farero-dev/farero/daemon/internal/model"
)

const sessionCols = `id, agent, agent_session_id, cwd, tty, pid, status, current_tool, tainted, started_at, last_event_at`

func scanSession(row interface{ Scan(...any) error }) (model.Session, error) {
	var s model.Session
	var tainted int
	var started, last int64
	err := row.Scan(&s.ID, &s.Agent, &s.AgentSessionID, &s.Cwd, &s.TTY, &s.PID, &s.Status,
		&s.CurrentTool, &tainted, &started, &last)
	s.Tainted = tainted != 0
	s.StartedAt = fromMs(started)
	s.LastEventAt = fromMs(last)
	return s, err
}

// UpsertSession inserts or fully replaces a session row.
func (s *Store) UpsertSession(ctx context.Context, x model.Session) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions(`+sessionCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
	cwd = excluded.cwd, tty = excluded.tty, pid = excluded.pid, status = excluded.status,
	current_tool = excluded.current_tool, tainted = excluded.tainted,
	last_event_at = excluded.last_event_at`,
		x.ID, x.Agent, x.AgentSessionID, x.Cwd, x.TTY, x.PID, x.Status, x.CurrentTool,
		boolInt(x.Tainted), ms(x.StartedAt), ms(x.LastEventAt))
	return err
}

// Session returns one session.
func (s *Store) Session(ctx context.Context, id string) (model.Session, error) {
	x, err := scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

// Sessions returns all sessions, newest first.
func (s *Store) Sessions(ctx context.Context) ([]model.Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionCols+` FROM sessions ORDER BY started_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Session
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// DeleteSession removes a session together with its hook events, calls and
// their search index entries.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddHookEvent records a hook event for a session.
func (s *Store) AddHookEvent(ctx context.Context, sessionID string, ts time.Time, event, tool string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO hook_events(session_id, ts, event, tool_name) VALUES(?,?,?,?)`,
		sessionID, ms(ts), event, tool)
	return err
}

// HookEventCount returns how many hook events a session has (used by tests
// and the session detail view).
func (s *Store) HookEventCount(ctx context.Context, sessionID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM hook_events WHERE session_id = ?`, sessionID).Scan(&n)
	return n, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
