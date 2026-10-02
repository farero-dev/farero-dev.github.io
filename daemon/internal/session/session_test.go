package session

import (
	"context"
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
	alive := func(pid int) bool { return pid == 200 }

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
