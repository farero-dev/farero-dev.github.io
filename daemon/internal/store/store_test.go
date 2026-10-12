package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/farero-dev/farero/daemon/internal/model"
)

func newMem(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addSession(t *testing.T, s *Store, id string) model.Session {
	t.Helper()
	now := time.Now()
	x := model.Session{ID: model.SessionKey("claude", id), Agent: "claude", AgentSessionID: id,
		Cwd: "/tmp/p", Status: model.StatusRunning, StartedAt: now, LastEventAt: now}
	if err := s.UpsertSession(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	return x
}

func addCall(t *testing.T, s *Store, sessionID, tool, input, result string) {
	t.Helper()
	_, err := s.InsertCall(context.Background(), model.Call{
		SessionID: sessionID, TS: time.Now(), Kind: model.KindPlugin, Agent: "claude",
		Plugin: "github", Tool: tool, Input: json.RawMessage(input), ResultText: result,
		Decision: model.DecisionAutoAllowed,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionRoundTripAndTaint(t *testing.T) {
	s := newMem(t)
	ctx := context.Background()
	x := addSession(t, s, "a1")
	x.Tainted = true
	x.Status = model.StatusWaitingInput
	if err := s.UpsertSession(ctx, x); err != nil {
		t.Fatal(err)
	}
	got, err := s.Session(ctx, x.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Tainted || got.Status != model.StatusWaitingInput || got.Cwd != "/tmp/p" {
		t.Fatalf("got %+v", got)
	}
	if _, err := s.Session(ctx, "claude:nope"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDeleteSessionCascades(t *testing.T) {
	s := newMem(t)
	ctx := context.Background()
	a := addSession(t, s, "a")
	b := addSession(t, s, "b")
	s.AddHookEvent(ctx, a.ID, time.Now(), "PreToolUse", "Bash")
	addCall(t, s, a.ID, "issue_read", `{"q":"deleted-session-marker"}`, "x")
	addCall(t, s, b.ID, "issue_read", `{"q":"kept-session-marker"}`, "y")
	addCall(t, s, "", "issue_read", `{"q":"unknown-session-marker"}`, "z")

	if _, err := s.DeleteSession(ctx, a.ID, false); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.HookEventCount(ctx, a.ID); n != 0 {
		t.Fatalf("hook events left: %d", n)
	}
	got, err := s.QueryCalls(ctx, CallFilter{Query: "deleted-session-marker"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("FTS still returns deleted call: %+v", got)
	}
	all, _ := s.QueryCalls(ctx, CallFilter{})
	if len(all) != 2 {
		t.Fatalf("want 2 calls left (other session + unknown), got %d", len(all))
	}
	if _, err := s.DeleteSession(ctx, a.ID, false); err != ErrNotFound {
		t.Fatalf("second delete: %v", err)
	}
}

func TestQueryCallsTrigramAndShortKorean(t *testing.T) {
	s := newMem(t)
	ctx := context.Background()
	a := addSession(t, s, "a")
	addCall(t, s, a.ID, "send-email", `{"subject":"배포 완료 알림","to":"ops@example.com"}`, "sent")
	addCall(t, s, a.ID, "issue_read", `{"title":"로그인 오류"}`, "body text")
	addCall(t, s, a.ID, "list_issues", `{"state":"open"}`, "100% done_ok")

	cases := []struct {
		q    string
		want int
	}{
		{"배포 완료", 1},   // trigram (5 runes incl. space)
		{"배포", 1},      // 2 runes → LIKE
		{"오류", 1},      // 2 runes → LIKE
		{"EXAMPLE", 1}, // trigram is case-insensitive
		{"issue", 2},   // matches tool names
		{"%", 1},       // LIKE metacharacters are escaped
		{"_", 2},       // two tool names contain "_"; unescaped it would match all 3
		{`"quoted`, 0}, // quotes do not break the FTS query
	}
	for _, c := range cases {
		got, err := s.QueryCalls(ctx, CallFilter{Query: c.q})
		if err != nil {
			t.Fatalf("%q: %v", c.q, err)
		}
		if len(got) != c.want {
			t.Errorf("%q: got %d rows, want %d", c.q, len(got), c.want)
		}
	}
	got, _ := s.QueryCalls(ctx, CallFilter{Tool: "issue_read"})
	if len(got) != 1 || got[0].Tool != "issue_read" {
		t.Fatalf("tool filter: %+v", got)
	}
	if string(got[0].Input) != `{"title":"로그인 오류"}` {
		t.Fatalf("input round trip: %s", got[0].Input)
	}
}

func TestTruncateUTF8(t *testing.T) {
	s := "가나다" // 3 bytes each
	if got := TruncateUTF8(s, 4); got != "가" {
		t.Fatalf("got %q", got)
	}
	if got := TruncateUTF8(s, 100); got != s {
		t.Fatalf("got %q", got)
	}
	// Exactly at the limit nothing is cut (and s[limit] is out of range),
	// and a limit of 0 or less means no limit.
	for _, limit := range []int{len(s), 0, -1} {
		if got := TruncateUTF8(s, limit); got != s {
			t.Fatalf("limit %d: got %q", limit, got)
		}
	}
	// A string that starts mid-rune cuts to nothing rather than walking
	// past its start.
	if got := TruncateUTF8("\x80\x80\x80\x80", 2); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestMigrationBackupAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "farero.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	addSession(t, s, "persist")
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := s2.Session(context.Background(), "claude:persist"); err != nil {
		t.Fatalf("session lost across reopen: %v", err)
	}
}

func TestPolicyOverridesAndPlugins(t *testing.T) {
	s := newMem(t)
	ctx := context.Background()
	s.SetPolicyOverride(ctx, "github_issue_write", "auto")
	s.SetPolicyOverride(ctx, "github_issue_write", "block")
	s.SetPolicyOverride(ctx, "railway_redeploy", "auto")
	s.SetPolicyOverride(ctx, "railway_redeploy", "")
	m, _ := s.PolicyOverrides(ctx)
	if len(m) != 1 || m["github_issue_write"] != "block" {
		t.Fatalf("overrides: %v", m)
	}
	p, _ := s.Plugin(ctx, "github")
	if p.Status != model.PluginDisconnected {
		t.Fatalf("default plugin status: %+v", p)
	}
	s.SavePlugin(ctx, model.PluginState{Plugin: "github", Status: model.PluginConnected,
		AccountLabel: "octocat", ConnectedAt: time.Now(), Options: map[string]string{"read_only": "true"}})
	p, _ = s.Plugin(ctx, "github")
	if p.Status != model.PluginConnected || p.AccountLabel != "octocat" || p.Options["read_only"] != "true" {
		t.Fatalf("plugin: %+v", p)
	}
}

func TestCallFilterAcceptsEmptyDates(t *testing.T) {
	var f CallFilter
	if err := json.Unmarshal([]byte(`{"query":"x","from":"","to":null,"limit":5}`), &f); err != nil {
		t.Fatal(err)
	}
	if f.Query != "x" || !f.From.IsZero() || !f.To.IsZero() || f.Limit != 5 {
		t.Fatalf("%+v", f)
	}
	if err := json.Unmarshal([]byte(`{"from":"2026-10-02T09:00:00+09:00"}`), &f); err != nil || f.From.IsZero() {
		t.Fatalf("%+v %v", f, err)
	}
}

func TestQueryCallsLimit(t *testing.T) {
	s := newMem(t)
	ctx := context.Background()
	for i := 0; i < MaxQueryLimit+1; i++ {
		addCall(t, s, "", "issue_read", `{}`, "")
	}
	for _, c := range []struct{ limit, want int }{
		{0, DefaultQueryLimit},
		{-1, DefaultQueryLimit},
		{3, 3},
		{MaxQueryLimit, MaxQueryLimit},
		{MaxQueryLimit + 1, MaxQueryLimit}, // clamped, not reset to the default
		{5000, MaxQueryLimit},
	} {
		got, err := s.QueryCalls(ctx, CallFilter{Limit: c.limit})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != c.want {
			t.Errorf("limit %d: got %d rows, want %d", c.limit, len(got), c.want)
		}
	}
}

func TestDeleteTaintedSessionLeavesTombstone(t *testing.T) {
	s := newMem(t)
	ctx := context.Background()
	clean := addSession(t, s, "clean")
	dirty := addSession(t, s, "dirty")
	dirty.Tainted = true
	s.UpsertSession(ctx, dirty)
	known := addSession(t, s, "known") // tainted in farerod's memory, not yet stored
	for _, c := range []struct {
		id      string
		tainted bool
		want    bool
	}{{clean.ID, false, false}, {dirty.ID, false, true}, {known.ID, true, true}} {
		left, err := s.DeleteSession(ctx, c.id, c.tainted)
		if err != nil || left != c.want {
			t.Fatalf("%s: tombstone %v %v, want %v", c.id, left, err, c.want)
		}
	}
	got, err := s.TaintTombstones(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[dirty.ID] || !got[known.ID] {
		t.Fatalf("tombstones: %v", got)
	}
	// A failed delete writes nothing.
	if _, err := s.DeleteSession(ctx, "claude:nope", true); err != ErrNotFound {
		t.Fatalf("missing session: %v", err)
	}
	if got, _ := s.TaintTombstones(ctx); got["claude:nope"] {
		t.Fatal("tombstone for a missing session")
	}
}

// A database from the first release (schema v1) is backed up and upgraded.
func TestMigrateFromV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "farero.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[0] + `PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(path + ".bak-v1"); err != nil {
		t.Fatalf("no backup: %v", err)
	}
	x := addSession(t, s, "old")
	x.Tainted = true
	s.UpsertSession(context.Background(), x)
	if _, err := s.DeleteSession(context.Background(), x.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.TaintTombstones(context.Background()); !got[x.ID] {
		t.Fatalf("tombstones: %v", got)
	}
}

func TestTrimJSON(t *testing.T) {
	// An input that fits comes back byte for byte, even right at the limit
	// and with key order or spacing that re-encoding would change.
	small := json.RawMessage(`{"b": 1, "a": "<x>"}`)
	for _, limit := range []int{100, len(small)} {
		if got := TrimJSON(small, limit); !bytes.Equal(got, small) {
			t.Fatalf("limit %d: small input changed: %s", limit, got)
		}
	}
	big := strings.Repeat("가", 5000) // 15000 bytes
	raw, _ := json.Marshal(map[string]any{"file_path": "/tmp/a.txt", "content": big, "n": 7, "list": []any{"short", big},
		"id": int64(9007199254740993)})
	if got := TrimJSON(raw, 0); !bytes.Equal(got, raw) {
		t.Fatalf("limit 0 means no limit, got %d bytes", len(got))
	}
	got := TrimJSON(raw, 4000)
	if len(got) > 4000 {
		t.Fatalf("not bounded: %d", len(got))
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("not JSON: %v: %s", err, got)
	}
	if m["file_path"] != "/tmp/a.txt" || m["n"] != float64(7) {
		t.Fatalf("short fields changed: %v", m)
	}
	// Numbers keep every digit (2^53+1 does not survive a float64).
	if !strings.Contains(string(got), `"id":9007199254740993`) {
		t.Fatalf("number changed: %s", got[:80])
	}
	// The kept part is whole runes (a split one would come back as U+FFFD,
	// which utf8.ValidString accepts) and the marker counts what is gone.
	content, _ := m["content"].(string)
	cut := regexp.MustCompile(`^(.*)…\[\+(\d+) bytes\]$`).FindStringSubmatch(content)
	if !utf8.ValidString(content) || cut == nil || !strings.HasPrefix(cut[1], "가가") || strings.Trim(cut[1], "가") != "" {
		t.Fatalf("content not cut on a rune boundary with a marker: %.40q", content)
	}
	if n, _ := strconv.Atoi(cut[2]); len(cut[1])+n != len(big) {
		t.Fatalf("marker says %s bytes gone, kept %d of %d", cut[2], len(cut[1]), len(big))
	}
	if l, _ := m["list"].([]any); len(l) != 2 || l[0] != "short" {
		t.Fatalf("list changed: %v", m["list"])
	}
	// Not JSON, or not shrinkable: a placeholder string.
	for _, in := range []json.RawMessage{json.RawMessage(strings.Repeat("x", 200)), json.RawMessage("[" + strings.Repeat("1,", 200) + "1]")} {
		var s string
		if err := json.Unmarshal(TrimJSON(in, 100), &s); err != nil || !strings.Contains(s, "bytes") {
			t.Fatalf("placeholder: %v %q", err, s)
		}
	}
}

// A huge input (a 40 MB attachment, a Write of a whole file) must not hold
// the write lock for seconds while the trigram index eats it. Indexing time
// follows the stored size, so the test checks the size: a wall-clock bound
// failed on GitHub's runners under -race (2.8 s for 1 MB).
func TestInsertCallBoundsInput(t *testing.T) {
	s := newMem(t)
	raw, _ := json.Marshal(map[string]any{"to": "a@b.c", "attachment": strings.Repeat("Zm9v", 1<<20)}) // 4 MB
	addCall(t, s, "", "send_email", string(raw), "ok")
	calls, err := s.QueryCalls(context.Background(), CallFilter{Limit: 1})
	if err != nil || len(calls) != 1 {
		t.Fatal(err, calls)
	}
	// 1 MB as a literal: a larger MaxStoredInput must fail here.
	if len(calls[0].Input) > 1<<20 {
		t.Fatalf("stored input %d bytes", len(calls[0].Input))
	}
	var m map[string]any
	if err := json.Unmarshal(calls[0].Input, &m); err != nil || m["to"] != "a@b.c" {
		t.Fatalf("stored input: %v %v", err, m)
	}
	if a, _ := m["attachment"].(string); !strings.HasSuffix(a, " bytes]") {
		t.Fatalf("attachment not cut: %d bytes", len(a))
	}
	// Still searchable by its short fields.
	if hits, _ := s.QueryCalls(context.Background(), CallFilter{Query: "a@b.c"}); len(hits) != 1 {
		t.Fatalf("search: %d hits", len(hits))
	}
}

// The budget is used, not just respected: a 20 KB string under a 16 KB
// limit keeps all but a few bytes of it.
func TestTrimJSONFillsBudget(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"file_path": "/tmp/a", "content": strings.Repeat("x", 20000)})
	got := TrimJSON(raw, DefaultResultLimit)
	if len(got) > DefaultResultLimit || len(got) < DefaultResultLimit-8 {
		t.Fatalf("got %d bytes, limit %d", len(got), DefaultResultLimit)
	}
}
