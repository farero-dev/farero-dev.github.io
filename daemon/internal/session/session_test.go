package session

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/store"
)

func newManager(t *testing.T) (*Manager, *store.Store, *[]model.Session) {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var updates []model.Session
	m, err := NewManager(context.Background(), st, func(s model.Session) { updates = append(updates, s) })
	if err != nil {
		t.Fatal(err)
	}
	return m, st, &updates
}

func ev(name, tool string) HookInput {
	return HookInput{SessionID: "s1", Cwd: "/Users/me/proj", HookEventName: name, ToolName: tool}
}

func TestStateMachine(t *testing.T) {
	m, st, updates := newManager(t)
	ctx := context.Background()
	steps := []struct {
		in     HookInput
		status string
		tool   string
	}{
		{ev(EventSessionStart, ""), model.StatusWaitingInput, ""},
		{ev(EventUserPromptSubmit, ""), model.StatusRunning, ""},
		{ev(EventPreToolUse, "Bash"), model.StatusRunning, "Bash"},
		{ev(EventPermissionRequest, "Bash"), model.StatusWaitingApproval, "Bash"},
		{ev(EventPostToolUse, "Bash"), model.StatusRunning, ""},
		{ev(EventNotification, ""), model.StatusWaitingInput, ""},
		{ev(EventUserPromptSubmit, ""), model.StatusRunning, ""},
		{ev(EventStop, ""), model.StatusWaitingInput, ""},
		{ev(EventSessionEnd, ""), model.StatusEnded, ""},
	}
	for i, s := range steps {
		got, err := m.Apply(ctx, model.AgentClaude, s.in, "ttys003", 4242)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != s.status || got.CurrentTool != s.tool {
			t.Fatalf("step %d %s: got %s/%q, want %s/%q", i, s.in.HookEventName, got.Status, got.CurrentTool, s.status, s.tool)
		}
	}
	if len(*updates) != len(steps) {
		t.Fatalf("updates: %d", len(*updates))
	}
	saved, err := st.Session(ctx, "claude:s1")
	if err != nil || saved.TTY != "ttys003" || saved.PID != 4242 || saved.Cwd != "/Users/me/proj" {
		t.Fatalf("saved: %+v %v", saved, err)
	}
	if n, _ := st.HookEventCount(ctx, "claude:s1"); n != len(steps) {
		t.Fatalf("hook events: %d", n)
	}
}

func TestFirstSeenMidSession(t *testing.T) {
	m, _, _ := newManager(t)
	got, err := m.Apply(context.Background(), model.AgentClaude, ev(EventPreToolUse, "Edit"), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusRunning || got.CurrentTool != "Edit" || got.StartedAt.IsZero() {
		t.Fatalf("got %+v", got)
	}
}

func TestTaintPersistsAcrossReload(t *testing.T) {
	m, st, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventSessionStart, ""), "", 0)
	m.MarkTainted(ctx, "claude:s1")
	m2, err := NewManager(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := m2.Get("claude:s1"); !s.Tainted {
		t.Fatal("taint lost after reload")
	}
}

func TestSweep(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	clock := time.Now()
	m.now = func() time.Time { return clock }
	m.Apply(ctx, model.AgentClaude, HookInput{SessionID: "dead", HookEventName: EventStop}, "", 100)
	m.Apply(ctx, model.AgentClaude, HookInput{SessionID: "alive", HookEventName: EventStop}, "", 200)
	m.Apply(ctx, model.AgentClaude, HookInput{SessionID: "ended", HookEventName: EventSessionEnd}, "", 300)
	alive := func(pid int, _ time.Time) bool { return pid == 200 }

	if dead := m.Sweep(ctx, alive); len(dead) != 0 {
		t.Fatalf("swept too early: %v", dead)
	}
	clock = clock.Add(UnknownAfter + time.Second)
	dead := m.Sweep(ctx, alive)
	if len(dead) != 1 || dead[0] != 100 {
		t.Fatalf("dead: %v", dead)
	}
	if s, _ := m.Get("claude:dead"); s.Status != model.StatusUnknown {
		t.Fatalf("dead session: %+v", s)
	}
	if s, _ := m.Get("claude:alive"); s.Status != model.StatusWaitingInput {
		t.Fatalf("alive session: %+v", s)
	}
	if s, _ := m.Get("claude:ended"); s.Status != model.StatusEnded {
		t.Fatalf("ended session: %+v", s)
	}
	// A new event brings an unknown session back into the state machine.
	got, _ := m.Apply(ctx, model.AgentClaude, HookInput{SessionID: "dead", HookEventName: EventUserPromptSubmit}, "", 100)
	if got.Status != model.StatusRunning {
		t.Fatalf("revived: %+v", got)
	}
}

func TestSetStatusIgnoresEnded(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventSessionEnd, ""), "", 0)
	m.SetStatus(ctx, "claude:s1", model.StatusRunning)
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusEnded {
		t.Fatalf("got %s", s.Status)
	}
}

func TestDelete(t *testing.T) {
	m, st, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventSessionStart, ""), "", 0)
	if err := m.Delete(ctx, "claude:s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("claude:s1"); ok {
		t.Fatal("still in memory")
	}
	if _, err := st.Session(ctx, "claude:s1"); err != store.ErrNotFound {
		t.Fatalf("still stored: %v", err)
	}
}

// Hooks run as separate processes, and SessionEnd is asynchronous, so an
// event of the exiting process can arrive after SessionEnd. It must not bring
// the session back.
func TestLateEventDoesNotReviveEnded(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventUserPromptSubmit, ""), "", 4242)
	m.Apply(ctx, model.AgentClaude, ev(EventSessionEnd, ""), "", 4242)
	for _, name := range []string{EventStop, EventPostToolUse, EventNotification} {
		got, err := m.Apply(ctx, model.AgentClaude, ev(name, ""), "", 4242)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.StatusEnded {
			t.Fatalf("%s revived the ended session: %s", name, got.Status)
		}
	}
}

// `claude --resume` runs a new process for the same session id, and the
// daemon may have missed its SessionStart (for example while it restarted).
func TestNewProcessRevivesEnded(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventSessionEnd, ""), "", 4242)
	got, _ := m.Apply(ctx, model.AgentClaude, ev(EventUserPromptSubmit, ""), "", 5151)
	if got.Status != model.StatusRunning || got.PID != 5151 {
		t.Fatalf("resumed session: %+v", got)
	}
}

func TestSessionStartRevivesEnded(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventSessionEnd, ""), "", 4242)
	in := ev(EventSessionStart, "")
	in.Source = "resume"
	got, _ := m.Apply(ctx, model.AgentClaude, in, "", 4242)
	if got.Status != model.StatusWaitingInput {
		t.Fatalf("got %s", got.Status)
	}
}

// Compaction can run in the middle of a turn; its SessionStart(compact) is
// not a new session waiting for a prompt.
func TestCompactKeepsStatus(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventPreToolUse, "Bash"), "", 4242)
	in := ev(EventSessionStart, "")
	in.Source = "compact"
	got, _ := m.Apply(ctx, model.AgentClaude, in, "", 4242)
	if got.Status != model.StatusRunning || got.CurrentTool != "Bash" {
		t.Fatalf("got %s/%q", got.Status, got.CurrentTool)
	}
}

// A PID that now belongs to a process started after the session's last
// event is not the agent: macOS reuses PIDs.
func TestSweepPassesLastEventTime(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	clock := time.Now()
	m.now = func() time.Time { return clock }
	m.Apply(ctx, model.AgentClaude, HookInput{SessionID: "s", HookEventName: EventStop}, "", 100)
	last := clock
	clock = clock.Add(UnknownAfter + time.Second)
	var got time.Time
	m.Sweep(ctx, func(pid int, before time.Time) bool { got = before; return false })
	if !got.Equal(last) {
		t.Fatalf("alive got %v, want the last event time %v", got, last)
	}
}

// AskUserQuestion goes through PermissionRequest, but it is a question for
// the user, not a permission: the session waits for input.
func TestQuestionWaitsForInput(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventPreToolUse, "AskUserQuestion"), "", 4242)
	got, _ := m.Apply(ctx, model.AgentClaude, ev(EventPermissionRequest, "AskUserQuestion"), "", 4242)
	if got.Status != model.StatusWaitingInput || got.CurrentTool != "AskUserQuestion" {
		t.Fatalf("got %s/%q", got.Status, got.CurrentTool)
	}
}

// An open approval card shows the session as waiting_approval, but the store
// keeps the status from hook events: cards do not survive farerod, so a
// reload must not show a card that is gone.
func TestOpenCardIsShownNotStored(t *testing.T) {
	m, st, updates := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, "claude", ev(EventSessionStart, ""), "", 0)
	m.ApprovalOpened(ctx, "claude:s1")
	m.Apply(ctx, "claude", ev(EventStop, ""), "", 0)
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusWaitingApproval {
		t.Fatalf("shown: %s", s.Status)
	}
	if last := (*updates)[len(*updates)-1]; last.Status != model.StatusWaitingApproval {
		t.Fatalf("sent to the app: %s", last.Status)
	}
	m2, err := NewManager(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := m2.Get("claude:s1"); s.Status != model.StatusWaitingInput {
		t.Fatalf("after reload: %s", s.Status)
	}
	m.ApprovalClosed(ctx, "claude:s1")
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusWaitingInput {
		t.Fatalf("after the card closed: %s", s.Status)
	}
}

// Deleting a session does not forget its open cards: if a hook event brings
// the session back, it still shows waiting_approval until they close.
func TestDeleteKeepsOpenCards(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, "claude", ev(EventSessionStart, ""), "", 0)
	m.ApprovalOpened(ctx, "claude:s1")
	if err := m.Delete(ctx, "claude:s1"); err != nil {
		t.Fatal(err)
	}
	m.Apply(ctx, "claude", ev(EventUserPromptSubmit, ""), "", 0)
	m.ApprovalOpened(ctx, "claude:s1")
	m.ApprovalClosed(ctx, "claude:s1")
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusWaitingApproval {
		t.Fatalf("one card still open: %s", s.Status)
	}
	m.ApprovalClosed(ctx, "claude:s1")
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusRunning {
		t.Fatalf("all cards closed: %s", s.Status)
	}
}

// The user can delete a session whose agent is still running; its next
// hook event creates it again. Deleting does not clear the agent's context,
// so a tainted session comes back tainted (Q39), also after a restart.
func TestDeletedTaintedSessionComesBackTainted(t *testing.T) {
	m, st, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventSessionStart, ""), "", 0)
	m.MarkTainted(ctx, "claude:s1")
	other := ev(EventSessionStart, "")
	other.SessionID = "s2"
	m.Apply(ctx, model.AgentClaude, other, "", 0)
	for _, id := range []string{"claude:s1", "claude:s2"} {
		if err := m.Delete(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	m2, err := NewManager(ctx, st, nil) // farerod restarted meanwhile
	if err != nil {
		t.Fatal(err)
	}
	for _, mgr := range []*Manager{m, m2} {
		got, err := mgr.Apply(ctx, model.AgentClaude, ev(EventPreToolUse, "Bash"), "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Tainted || got.Status != model.StatusRunning {
			t.Fatalf("re-created: %+v", got)
		}
		other.HookEventName = EventUserPromptSubmit
		if got, _ := mgr.Apply(ctx, model.AgentClaude, other, "", 0); got.Tainted {
			t.Fatalf("clean session came back tainted: %+v", got)
		}
	}
	if saved, _ := st.Session(ctx, "claude:s1"); !saved.Tainted {
		t.Fatalf("stored: %+v", saved)
	}
	// Deleted again (now tainted in the row): still remembered.
	if err := m.Delete(ctx, "claude:s1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Apply(ctx, model.AgentClaude, ev(EventStop, ""), "", 0); !got.Tainted {
		t.Fatalf("second re-creation: %+v", got)
	}
}

// A call answered after its session was deleted brings the session back,
// so the call's log row has a session to belong to.
func TestReviveAfterDelete(t *testing.T) {
	m, st, updates := newManager(t)
	ctx := context.Background()
	prev, _ := m.Apply(ctx, model.AgentClaude, ev(EventPermissionRequest, "Bash"), "ttys007", 4242)
	m.MarkTainted(ctx, "claude:s1") // after the snapshot: the tombstone carries it
	if err := m.Delete(ctx, "claude:s1"); err != nil {
		t.Fatal(err)
	}
	n := len(*updates)
	if err := m.Revive(ctx, prev); err != nil {
		t.Fatal(err)
	}
	got, ok := m.Get("claude:s1")
	if !ok || !got.Tainted || got.Cwd != prev.Cwd || got.TTY != "ttys007" || got.PID != 4242 ||
		got.Status != model.StatusRunning || got.Agent != model.AgentClaude || got.AgentSessionID != "s1" {
		t.Fatalf("revived: %+v", got)
	}
	if len(*updates) != n+1 || (*updates)[n].ID != "claude:s1" {
		t.Fatalf("revive not published: %+v", (*updates)[n:])
	}
	if saved, err := st.Session(ctx, "claude:s1"); err != nil || !saved.Tainted {
		t.Fatalf("stored: %+v %v", saved, err)
	}
	// An existing session is left alone.
	m.Apply(ctx, model.AgentClaude, ev(EventStop, ""), "", 4242)
	n = len(*updates)
	m.Revive(ctx, prev)
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusWaitingInput || len(*updates) != n {
		t.Fatalf("revive changed a live session: %+v", s)
	}
	// A snapshot that knew the taint is enough without a tombstone.
	other := ev(EventSessionStart, "")
	other.SessionID = "s2"
	snap, _ := m.Apply(ctx, model.AgentClaude, other, "", 0)
	m.Delete(ctx, "claude:s2")
	snap.Tainted = true
	m.Revive(ctx, snap)
	if s, _ := m.Get("claude:s2"); !s.Tainted {
		t.Fatalf("taint from snapshot lost: %+v", s)
	}
}

// Sweep's limits are exact: a dead agent is unknown ExitGrace after it was
// first seen dead, a session without a PID UnknownAfter after its last
// event, and a session without a PID never goes through alive().
func TestSweepBoundaries(t *testing.T) {
	ctx := context.Background()
	t.Run("exit grace", func(t *testing.T) {
		m, _, _ := newManager(t)
		clock := time.Now()
		m.now = func() time.Time { return clock }
		m.Apply(ctx, model.AgentClaude, HookInput{SessionID: "d", HookEventName: EventStop}, "", 100)
		dead := func(int, time.Time) bool { return false }
		m.Sweep(ctx, dead) // first seen dead
		clock = clock.Add(ExitGrace - time.Millisecond)
		if got := m.Sweep(ctx, dead); len(got) != 0 {
			t.Fatalf("before the grace: %v", got)
		}
		clock = clock.Add(time.Millisecond)
		if got := m.Sweep(ctx, dead); len(got) != 1 || got[0] != 100 {
			t.Fatalf("at the grace: %v", got)
		}
	})
	t.Run("silence", func(t *testing.T) {
		m, _, _ := newManager(t)
		clock := time.Now()
		m.now = func() time.Time { return clock }
		m.Apply(ctx, model.AgentClaude, HookInput{SessionID: "q", HookEventName: EventStop}, "", 0)
		alive := func(pid int, _ time.Time) bool {
			t.Errorf("alive(%d) for a session without a PID", pid)
			return false
		}
		clock = clock.Add(ExitGrace + time.Second)
		m.Sweep(ctx, alive)
		clock = clock.Add(UnknownAfter - ExitGrace - time.Second - time.Millisecond)
		m.Sweep(ctx, alive)
		if s, _ := m.Get("claude:q"); s.Status != model.StatusWaitingInput {
			t.Fatalf("before UnknownAfter: %s", s.Status)
		}
		clock = clock.Add(time.Millisecond)
		m.Sweep(ctx, alive)
		if s, _ := m.Get("claude:q"); s.Status != model.StatusUnknown {
			t.Fatalf("at UnknownAfter: %s", s.Status)
		}
	})
}

// One Sweep handles every session, whatever comes before it in the map:
// ended, alive, newly dead and recently heard sessions are skipped, and
// both a dead agent and a silent session still become unknown.
func TestSweepVisitsEverySession(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	clock := time.Now()
	m.now = func() time.Time { return clock }
	apply := func(id, event string, pid int) {
		m.Apply(ctx, model.AgentClaude, HookInput{SessionID: id, HookEventName: event}, "", pid)
	}
	alive := map[int]bool{}
	for i := 0; i < 10; i++ {
		apply(fmt.Sprintf("ended%d", i), EventSessionEnd, 1000+i)
		apply(fmt.Sprintf("alive%d", i), EventStop, 2000+i)
		alive[2000+i] = true
	}
	apply("dead", EventStop, 100)
	apply("silent", EventStop, 0)
	isAlive := func(pid int, _ time.Time) bool { return alive[pid] }
	m.Sweep(ctx, isAlive) // "dead" is first seen dead
	clock = clock.Add(UnknownAfter)
	for i := 0; i < 10; i++ {
		apply(fmt.Sprintf("new%d", i), EventStop, 3000+i) // dead, first seen now
		apply(fmt.Sprintf("heard%d", i), EventStop, 0)    // no PID, just heard from
	}
	got := m.Sweep(ctx, isAlive)
	if len(got) != 1 || got[0] != 100 {
		t.Fatalf("dead PIDs: %v", got)
	}
	for _, id := range []string{"dead", "silent"} {
		if s, _ := m.Get("claude:" + id); s.Status != model.StatusUnknown {
			t.Errorf("%s: %s", id, s.Status)
		}
	}
	for _, id := range []string{"alive3", "new3", "heard3"} {
		if s, _ := m.Get("claude:" + id); s.Status != model.StatusWaitingInput {
			t.Errorf("%s: %s", id, s.Status)
		}
	}
}

// Opening and closing cards publishes only when the shown status changes:
// the first card and the last one closing, not the ones in between.
func TestApprovalCountPublishesOnChange(t *testing.T) {
	m, _, updates := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventStop, ""), "", 0)
	want := []string{model.StatusWaitingApproval, "", "", model.StatusWaitingInput}
	steps := []func(context.Context, string){m.ApprovalOpened, m.ApprovalOpened, m.ApprovalClosed, m.ApprovalClosed}
	for i, step := range steps {
		n := len(*updates)
		step(ctx, "claude:s1")
		switch {
		case want[i] == "" && len(*updates) != n:
			t.Fatalf("step %d: unexpected update %+v", i, (*updates)[n:])
		case want[i] != "" && (len(*updates) != n+1 || (*updates)[n].Status != want[i]):
			t.Fatalf("step %d: updates %+v, want one with %s", i, (*updates)[n:], want[i])
		}
	}
}

// SetStatus changes the status and tells the app once; setting the same
// status again does nothing.
func TestSetStatusChangesOnce(t *testing.T) {
	m, _, updates := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventPermissionRequest, "Bash"), "", 0)
	n := len(*updates)
	m.SetStatus(ctx, "claude:s1", model.StatusRunning)
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusRunning || len(*updates) != n+1 {
		t.Fatalf("status %s, %d updates", s.Status, len(*updates)-n)
	}
	m.SetStatus(ctx, "claude:s1", model.StatusRunning)
	if len(*updates) != n+1 {
		t.Fatalf("repeated: %d updates", len(*updates)-n)
	}
}

// A taint that is only in memory so far still leaves a tombstone when the
// session is deleted (Q39).
func TestDeleteUsesTaintInMemory(t *testing.T) {
	m, st, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventUserPromptSubmit, ""), "", 0)
	m.mu.Lock()
	m.sessions["claude:s1"].Tainted = true // not published yet
	m.mu.Unlock()
	if err := m.Delete(ctx, "claude:s1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.TaintTombstones(ctx); !got["claude:s1"] {
		t.Fatalf("tombstones: %v", got)
	}
}
