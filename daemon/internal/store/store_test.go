package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

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

	if err := s.DeleteSession(ctx, a.ID); err != nil {
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
	if err := s.DeleteSession(ctx, a.ID); err != ErrNotFound {
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
