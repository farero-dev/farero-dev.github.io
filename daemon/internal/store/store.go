// Package store keeps farerod's persistent state in SQLite: sessions, hook
// events, the audit log of calls (with a trigram full-text index), policy
// overrides, plugin status and small settings.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps the farero SQLite database.
type Store struct {
	db *sql.DB
}

// migrations[i] upgrades the schema from user_version i to i+1.
var migrations = []string{
	`
CREATE TABLE sessions (
	id               TEXT PRIMARY KEY,
	agent            TEXT NOT NULL,
	agent_session_id TEXT NOT NULL,
	cwd              TEXT NOT NULL DEFAULT '',
	tty              TEXT NOT NULL DEFAULT '',
	pid              INTEGER NOT NULL DEFAULT 0,
	status           TEXT NOT NULL,
	current_tool     TEXT NOT NULL DEFAULT '',
	tainted          INTEGER NOT NULL DEFAULT 0,
	started_at       INTEGER NOT NULL,
	last_event_at    INTEGER NOT NULL
);
CREATE TABLE hook_events (
	id         INTEGER PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	ts         INTEGER NOT NULL,
	event      TEXT NOT NULL,
	tool_name  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX hook_events_session ON hook_events(session_id, ts);
CREATE TABLE calls (
	id           INTEGER PRIMARY KEY,
	session_id   TEXT REFERENCES sessions(id) ON DELETE CASCADE,
	conn_id      TEXT NOT NULL DEFAULT '',
	ts           INTEGER NOT NULL,
	kind         TEXT NOT NULL,
	agent        TEXT NOT NULL DEFAULT '',
	plugin       TEXT NOT NULL DEFAULT '',
	tool         TEXT NOT NULL,
	input_json   TEXT NOT NULL DEFAULT '',
	result_text  TEXT NOT NULL DEFAULT '',
	result_bytes INTEGER NOT NULL DEFAULT 0,
	decision     TEXT NOT NULL,
	reason       TEXT NOT NULL DEFAULT '',
	duration_ms  INTEGER NOT NULL DEFAULT 0,
	error        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX calls_session ON calls(session_id, ts);
CREATE INDEX calls_ts ON calls(ts);
CREATE VIRTUAL TABLE calls_fts USING fts5(
	tool, input_json, result_text,
	content='calls', content_rowid='id', tokenize='trigram'
);
CREATE TRIGGER calls_ai AFTER INSERT ON calls BEGIN
	INSERT INTO calls_fts(rowid, tool, input_json, result_text)
	VALUES (new.id, new.tool, new.input_json, new.result_text);
END;
CREATE TRIGGER calls_ad AFTER DELETE ON calls BEGIN
	INSERT INTO calls_fts(calls_fts, rowid, tool, input_json, result_text)
	VALUES ('delete', old.id, old.tool, old.input_json, old.result_text);
END;
CREATE TRIGGER calls_au AFTER UPDATE ON calls BEGIN
	INSERT INTO calls_fts(calls_fts, rowid, tool, input_json, result_text)
	VALUES ('delete', old.id, old.tool, old.input_json, old.result_text);
	INSERT INTO calls_fts(rowid, tool, input_json, result_text)
	VALUES (new.id, new.tool, new.input_json, new.result_text);
END;
CREATE TABLE policy_overrides (
	tool_key TEXT PRIMARY KEY,
	level    TEXT NOT NULL
);
CREATE TABLE plugins (
	plugin        TEXT PRIMARY KEY,
	status        TEXT NOT NULL,
	account_label TEXT NOT NULL DEFAULT '',
	connected_at  INTEGER NOT NULL DEFAULT 0,
	options       TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`,
}

// SchemaVersion is the user_version a fully migrated database has.
var SchemaVersion = len(migrations)

// Open opens (creating if needed) the database at path and migrates it.
// Before migrating an existing database it copies the file to
// path + ".bak-v<old version>".
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(path); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenMemory opens a private in-memory database, for tests.
func OpenMemory() (*Store, error) {
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// Every pooled connection to :memory: would be a separate database.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(""); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate(path string) error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("database schema v%d is newer than this farerod (v%d)", v, len(migrations))
	}
	if v == len(migrations) {
		return nil
	}
	if v > 0 && path != "" {
		if err := copyFile(path, fmt.Sprintf("%s.bak-v%d", path, v)); err != nil {
			return fmt.Errorf("backup before migration: %w", err)
		}
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMs(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Setting returns a settings value, or "" when unset.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting stores a settings value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}
