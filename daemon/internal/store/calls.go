package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/farero-dev/farero/daemon/internal/model"
)

// DefaultResultLimit is the per-call result size stored in the log (Q46).
const DefaultResultLimit = 16 * 1024

// TruncateUTF8 cuts s to at most limit bytes without splitting a rune.
func TruncateUTF8(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// InsertCall appends an audit log row and returns its id.
func (s *Store) InsertCall(ctx context.Context, c model.Call) (int64, error) {
	var sid any
	if c.SessionID != "" {
		sid = c.SessionID
	}
	input := string(c.Input)
	res, err := s.db.ExecContext(ctx, `
INSERT INTO calls(session_id, conn_id, ts, kind, agent, plugin, tool, input_json, result_text,
	result_bytes, decision, reason, duration_ms, error)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sid, c.ConnID, ms(c.TS), c.Kind, c.Agent, c.Plugin, c.Tool, input, c.ResultText,
		c.ResultBytes, c.Decision, c.Reason, c.DurationMS, c.Error)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CallFilter selects audit log rows. Zero values mean "any".
type CallFilter struct {
	Query     string    `json:"query"`
	SessionID string    `json:"session_id"`
	Agent     string    `json:"agent"`
	Plugin    string    `json:"plugin"`
	Tool      string    `json:"tool"`
	Decision  string    `json:"decision"`
	Kind      string    `json:"kind"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Limit     int       `json:"limit"`
	Offset    int       `json:"offset"`
}

// QueryCalls searches the audit log, newest first.
//
// Queries of three or more characters use the FTS5 trigram index; shorter
// ones (common for Korean, where two syllables are a word) fall back to LIKE
// because a trigram index cannot match fewer than three characters (Q64).
func (s *Store) QueryCalls(ctx context.Context, f CallFilter) ([]model.Call, error) {
	var where []string
	var args []any
	from := `calls c`
	if q := strings.TrimSpace(f.Query); q != "" {
		if utf8.RuneCountInString(q) >= 3 {
			from = `calls_fts JOIN calls c ON c.id = calls_fts.rowid`
			where = append(where, `calls_fts MATCH ?`)
			args = append(args, `"`+strings.ReplaceAll(q, `"`, `""`)+`"`)
		} else {
			pat := "%" + escapeLike(q) + "%"
			where = append(where, `(c.tool LIKE ? ESCAPE '\' OR c.input_json LIKE ? ESCAPE '\' OR c.result_text LIKE ? ESCAPE '\')`)
			args = append(args, pat, pat, pat)
		}
	}
	eq := func(col, v string) {
		if v != "" {
			where = append(where, col+` = ?`)
			args = append(args, v)
		}
	}
	eq("c.session_id", f.SessionID)
	eq("c.agent", f.Agent)
	eq("c.plugin", f.Plugin)
	eq("c.tool", f.Tool)
	eq("c.decision", f.Decision)
	eq("c.kind", f.Kind)
	if !f.From.IsZero() {
		where = append(where, `c.ts >= ?`)
		args = append(args, ms(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, `c.ts < ?`)
		args = append(args, ms(f.To))
	}
	q := `SELECT c.id, c.session_id, c.conn_id, c.ts, c.kind, c.agent, c.plugin, c.tool, c.input_json,
	c.result_text, c.result_bytes, c.decision, c.reason, c.duration_ms, c.error FROM ` + from
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q += ` ORDER BY c.ts DESC, c.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Call
	for rows.Next() {
		var c model.Call
		var sid sql.NullString
		var ts int64
		var input string
		if err := rows.Scan(&c.ID, &sid, &c.ConnID, &ts, &c.Kind, &c.Agent, &c.Plugin, &c.Tool, &input,
			&c.ResultText, &c.ResultBytes, &c.Decision, &c.Reason, &c.DurationMS, &c.Error); err != nil {
			return nil, err
		}
		c.SessionID = sid.String
		c.TS = fromMs(ts)
		if json.Valid([]byte(input)) {
			c.Input = json.RawMessage(input)
		} else if input != "" {
			b, _ := json.Marshal(input)
			c.Input = b
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
