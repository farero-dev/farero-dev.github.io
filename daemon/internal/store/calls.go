package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/farero-dev/farero/daemon/internal/model"
)

// Per-call result size stored in the log (Q46): the default and the range
// the user can choose from.
const (
	DefaultResultLimit = 16 << 10
	MinResultLimit     = 1 << 10
	MaxResultLimit     = 1 << 20
)

// MaxStoredInput is the ceiling InsertCall puts on input_json whatever the
// caller asked for. The trigram FTS trigger indexes input_json inside the
// insert, at roughly 0.4 s per MB while holding the write lock; a 40 MB
// Resend attachment would take about 16 s and other writers (sessions,
// hook events) would fail with SQLITE_BUSY after busy_timeout (5 s). 1 MB,
// the largest Q46 result budget, indexes in well under a second.
const MaxStoredInput = MaxResultLimit

// Log query page sizes.
const (
	DefaultQueryLimit = 200
	MaxQueryLimit     = 1000
)

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

// TrimJSON bounds a tool input that is stored in the log or sent to the
// app. Up to limit bytes it is returned as is; a limit of 0 or less means
// no limit. A larger input keeps its
// structure and loses the tails of its long string values (a Write's
// content, an attachment's base64), each cut on a rune boundary and marked
// with how many bytes are gone, so the result is still JSON the app decodes
// and the short fields (command, file_path) stay intact and searchable.
// An input that is not valid JSON, or that is too large even with every
// string emptied, becomes a placeholder string naming its size.
//
// The full input is untouched: policy and correlation hash the original,
// and the upstream tool receives the original.
func TrimJSON(raw json.RawMessage, limit int) json.RawMessage {
	if limit <= 0 || len(raw) <= limit {
		return raw
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err == nil {
		// Every string gets the same per-string budget; short values
		// survive as long as the budget is above their size. Halve it until
		// the whole thing fits, then search for the largest budget that
		// still does.
		fits := func(per int) (json.RawMessage, bool) {
			out, err := json.Marshal(shrinkStrings(v, per))
			return out, err == nil && len(out) <= limit
		}
		lo, hi := limit, limit
		best, ok := fits(lo)
		for !ok {
			if lo == 0 {
				return placeholder(len(raw))
			}
			hi = lo
			lo /= 2
			best, ok = fits(lo)
		}
		for lo+1 < hi {
			mid := (lo + hi) / 2
			if out, ok := fits(mid); ok {
				lo, best = mid, out
			} else {
				hi = mid
			}
		}
		return best
	}
	return placeholder(len(raw))
}

func placeholder(n int) json.RawMessage {
	out, _ := json.Marshal(fmt.Sprintf("(입력 %d bytes 생략)", n))
	return out
}

func shrinkStrings(v any, per int) any {
	switch x := v.(type) {
	case string:
		if len(x) <= per {
			return x
		}
		cut := TruncateUTF8(x, per)
		return fmt.Sprintf("%s…[+%d bytes]", cut, len(x)-len(cut))
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = shrinkStrings(e, per)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = shrinkStrings(e, per)
		}
		return out
	}
	return v
}

// InsertCall appends an audit log row and returns its id. input_json is
// bounded by MaxStoredInput (see there); callers apply the user's smaller
// Q46 budget themselves.
func (s *Store) InsertCall(ctx context.Context, c model.Call) (int64, error) {
	var sid any
	if c.SessionID != "" {
		sid = c.SessionID
	}
	input := string(TrimJSON(c.Input, MaxStoredInput))
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

// SetCallDecision rewrites a row's decision and reason, for a decision that
// turned out not to be the one the agent followed.
func (s *Store) SetCallDecision(ctx context.Context, id int64, decision, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE calls SET decision = ?, reason = ? WHERE id = ?`, decision, reason, id)
	return err
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
	From      time.Time `json:"from,omitzero"`
	To        time.Time `json:"to,omitzero"`
	Limit     int       `json:"limit"`
	Offset    int       `json:"offset"`
}

// UnmarshalJSON accepts "" (and null) for from/to, which clients send for
// "no bound".
func (f *CallFilter) UnmarshalJSON(b []byte) error {
	type plain CallFilter
	var raw struct {
		plain
		From any `json:"from"`
		To   any `json:"to"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*f = CallFilter(raw.plain)
	parse := func(v any) (time.Time, error) {
		s, _ := v.(string)
		if s == "" {
			return time.Time{}, nil
		}
		return time.Parse(time.RFC3339Nano, s)
	}
	var err error
	if f.From, err = parse(raw.From); err != nil {
		return err
	}
	f.To, err = parse(raw.To)
	return err
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
	switch {
	case limit <= 0:
		limit = DefaultQueryLimit
	case limit > MaxQueryLimit:
		limit = MaxQueryLimit
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
