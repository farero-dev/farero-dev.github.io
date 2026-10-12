package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/model"
)

func notification(kind string) HookInput {
	in := ev(EventNotification, "")
	in.NotificationType = kind
	return in
}

// Notification types seen from Claude Code 2.1.290 (M2 실험).
func TestNotificationTypes(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		before HookInput
		kind   string
		want   string
	}{
		// Sent about 6 s after an unanswered PermissionRequest: still waiting
		// for approval, not for input.
		{"permission prompt while waiting", ev(EventPermissionRequest, "Bash"), "permission_prompt", model.StatusWaitingApproval},
		// No app: the terminal shows the prompt.
		{"permission prompt while running", ev(EventPreToolUse, "Bash"), "permission_prompt", model.StatusWaitingApproval},
		{"idle", ev(EventPreToolUse, "Bash"), "idle_prompt", model.StatusWaitingInput},
		{"elicitation", ev(EventPreToolUse, "mcp__x__y"), "elicitation_dialog", model.StatusWaitingInput},
		{"elicitation url", ev(EventPreToolUse, "mcp__x__y"), "elicitation_url_dialog", model.StatusWaitingInput},
		{"auth success changes nothing", ev(EventPreToolUse, "Bash"), "auth_success", model.StatusRunning},
		{"unknown kind changes nothing", ev(EventPreToolUse, "Bash"), "something_new", model.StatusRunning},
		// Older Claude Code without notification_type.
		{"legacy while running", ev(EventPreToolUse, "Bash"), "", model.StatusWaitingInput},
		{"legacy while waiting for approval", ev(EventPermissionRequest, "Bash"), "", model.StatusWaitingApproval},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, _, _ := newManager(t)
			m.Apply(ctx, model.AgentClaude, c.before, "", 4242)
			got, _ := m.Apply(ctx, model.AgentClaude, notification(c.kind), "", 4242)
			if got.Status != c.want {
				t.Fatalf("got %s, want %s", got.Status, c.want)
			}
		})
	}
}

func TestElicitationCompleteResumes(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, notification("elicitation_dialog"), "", 4242)
	got, _ := m.Apply(ctx, model.AgentClaude, notification("elicitation_complete"), "", 4242)
	if got.Status != model.StatusRunning {
		t.Fatalf("got %s", got.Status)
	}
}

// StopFailure ends the turn instead of Stop when the API call fails.
func TestStopFailureWaitsForInput(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventUserPromptSubmit, ""), "", 4242)
	got, _ := m.Apply(ctx, model.AgentClaude, ev(EventStopFailure, ""), "", 4242)
	if got.Status != model.StatusWaitingInput {
		t.Fatalf("got %s", got.Status)
	}
}

// Claude Code's internal forks (compaction, prompt suggestions, background
// summaries) send PreToolUse with agent_id and no agent_type, for tools that
// never run. They must not touch the session.
func TestForkEventsAreIgnored(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventStop, ""), "", 4242)
	fork := ev(EventPreToolUse, "Bash")
	fork.AgentID = "a47080ec2a632bdbe"
	if !fork.Fork() {
		t.Fatal("agent_id without agent_type is a fork")
	}
	got, _ := m.Apply(ctx, model.AgentClaude, fork, "", 4242)
	if got.Status != model.StatusWaitingInput || got.CurrentTool != "" {
		t.Fatalf("fork changed the session: %s/%q", got.Status, got.CurrentTool)
	}
	// A real subagent has agent_type and does work for the session.
	sub := ev(EventPreToolUse, "Bash")
	sub.AgentID = "abd87a99eb0f11e45"
	kind := "general-purpose"
	sub.AgentType = &kind
	if sub.Fork() {
		t.Fatal("subagent is not a fork")
	}
	got, _ = m.Apply(ctx, model.AgentClaude, sub, "", 4242)
	if got.Status != model.StatusRunning || got.CurrentTool != "Bash" {
		t.Fatalf("subagent: %s/%q", got.Status, got.CurrentTool)
	}
}

// --- interrupts (Esc, Ctrl-C, denying in the terminal send no hook) ---

const (
	rowPrompt    = `{"type":"user","message":{"role":"user","content":"Run ping"}}`
	rowToolUse   = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{}}]}}`
	rowRejected  = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"The user doesn't want to proceed with this tool use."}]}}`
	rowInterrupt = `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`
	rowDuration  = `{"type":"system","subtype":"turn_duration","durationMs":1234}`
)

func writeTranscript(t *testing.T, path string, rows ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(rows, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendTranscript(t *testing.T, path string, rows ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(strings.Join(rows, "\n") + "\n")
}

func TestTranscriptInterrupted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	// Bookkeeping rows after the marker, larger than one read chunk.
	big := `{"type":"file-history-snapshot","snapshot":"` + strings.Repeat("x", 300<<10) + `"}`
	cost := `{"type":"cost-state","totalCostUSD":0.01}`
	for _, c := range []struct {
		name string
		rows []string
		want bool
	}{
		{"tool running", []string{rowPrompt, rowToolUse}, false},
		{"denied in terminal", []string{rowPrompt, rowToolUse, rowRejected, rowInterrupt, rowDuration, cost}, true},
		{"interrupted while streaming", []string{rowPrompt, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}`}, true},
		{"marker before big rows", []string{rowPrompt, rowToolUse, rowRejected, rowInterrupt, big, cost}, true},
		{"next prompt after the marker", []string{rowInterrupt, rowPrompt}, false},
		{"empty", nil, false},
		// The first row of the file counts (there is no partial line before it).
		{"marker is the first row", []string{rowInterrupt}, true},
		// More than the 1 MB chunk of bookkeeping after the marker: the last,
		// 8 MB chunk still finds it.
		{"marker before more than 1 MB", []string{rowPrompt, rowInterrupt, big, big, big, big, cost}, true},
		// A text block counts only when it is the marker.
		{"other text block", []string{rowInterrupt, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"keep going"}]}}`}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			writeTranscript(t, path, c.rows...)
			if got := transcriptInterrupted(path, 0); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
	if transcriptInterrupted(filepath.Join(dir, "missing.jsonl"), 0) {
		t.Fatal("missing file")
	}
}

// Only a marker written after the given offset (the transcript's size at
// the session's last event) belongs to the current turn.
func TestTranscriptInterruptedAfterOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeTranscript(t, path, rowPrompt, rowToolUse, rowRejected, rowInterrupt)
	fi, _ := os.Stat(path)
	if transcriptInterrupted(path, fi.Size()) {
		t.Fatal("marker of an earlier turn")
	}
	// Bookkeeping written after a new prompt, before its user row.
	appendTranscript(t, path, `{"type":"file-history-snapshot","snapshot":"x"}`)
	if transcriptInterrupted(path, fi.Size()) {
		t.Fatal("marker of an earlier turn behind new rows")
	}
}

// A row still being written is no answer yet.
func TestTranscriptPartialLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeTranscript(t, path, rowPrompt, rowInterrupt)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"type":"user","message":{"role":"user","content":"next pro`)
	f.Close()
	if transcriptInterrupted(path, 0) {
		t.Fatal("decided on a partial row")
	}
}

func TestCheckInterrupts(t *testing.T) {
	m, _, updates := newManager(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	// An earlier turn ended with an interrupt.
	writeTranscript(t, path, rowPrompt, rowToolUse, rowRejected, rowInterrupt)

	in := ev(EventUserPromptSubmit, "")
	in.TranscriptPath = path
	m.Apply(ctx, model.AgentClaude, in, "", 4242)
	appendTranscript(t, path, `{"type":"file-history-snapshot","snapshot":"x"}`)
	m.CheckInterrupts(ctx)
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusRunning {
		t.Fatalf("stale marker: %s", s.Status)
	}

	in = ev(EventPreToolUse, "Bash")
	in.TranscriptPath = path
	m.Apply(ctx, model.AgentClaude, in, "", 4242)
	appendTranscript(t, path, rowToolUse, rowRejected, rowInterrupt)
	n := len(*updates)
	m.CheckInterrupts(ctx)
	s, _ := m.Get("claude:s1")
	if s.Status != model.StatusWaitingInput || s.CurrentTool != "" {
		t.Fatalf("after interrupt: %s/%q", s.Status, s.CurrentTool)
	}
	if len(*updates) != n+1 {
		t.Fatal("no update sent")
	}
	m.CheckInterrupts(ctx)
	if len(*updates) != n+1 {
		t.Fatal("repeated update")
	}
}

// A Notification that arrives after the marker (permission_prompt comes 6 s
// after an unanswered PermissionRequest) does not hide it.
func TestInterruptAfterNotification(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeTranscript(t, path, rowPrompt, rowToolUse)
	in := ev(EventPermissionRequest, "Bash")
	in.TranscriptPath = path
	m.Apply(ctx, model.AgentClaude, in, "", 4242)
	appendTranscript(t, path, rowRejected, rowInterrupt)
	note := notification("permission_prompt")
	note.TranscriptPath = path
	m.Apply(ctx, model.AgentClaude, note, "", 4242)
	m.CheckInterrupts(ctx)
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusWaitingInput {
		t.Fatalf("got %s", s.Status)
	}
}

// A hook still running after claude exited is reparented to launchd and
// reports PID 1 (or 0 from farero-hook): that is no agent process.
func TestOrphanHookPIDIsIgnored(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventUserPromptSubmit, ""), "", 4242)
	m.Apply(ctx, model.AgentClaude, ev(EventSessionEnd, ""), "", 4242)
	for _, pid := range []int{1, 0} {
		got, _ := m.Apply(ctx, model.AgentClaude, ev(EventStop, ""), "", pid)
		if got.Status != model.StatusEnded || got.PID != 4242 {
			t.Fatalf("pid %d: %s pid=%d", pid, got.Status, got.PID)
		}
	}
}

// idle_prompt does not hide a card of a background subagent.
func TestIdleKeepsApproval(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	m.Apply(ctx, model.AgentClaude, ev(EventPermissionRequest, "Bash"), "", 4242)
	got, _ := m.Apply(ctx, model.AgentClaude, notification("idle_prompt"), "", 4242)
	if got.Status != model.StatusWaitingApproval {
		t.Fatalf("got %s", got.Status)
	}
}

// The agent process exited without SessionEnd (M2 실험: /exit while a
// PermissionRequest hook waits sends none). Once the process is confirmed
// gone, the session becomes unknown without waiting UnknownAfter.
func TestSweepDeadProcess(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	clock := time.Now()
	m.now = func() time.Time { return clock }
	m.Apply(ctx, model.AgentClaude, ev(EventPermissionRequest, "Bash"), "", 100)
	dead := func(int, time.Time) bool { return false }

	m.Sweep(ctx, dead) // first seen gone: a SessionEnd may still be on its way
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusWaitingApproval {
		t.Fatalf("too early: %s", s.Status)
	}
	clock = clock.Add(ExitGrace + time.Second)
	if got := m.Sweep(ctx, dead); len(got) != 1 || got[0] != 100 {
		t.Fatalf("dead pids: %v", got)
	}
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusUnknown {
		t.Fatalf("got %s", s.Status)
	}
}

// Without a PID only silence counts.
func TestSweepWithoutPID(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	clock := time.Now()
	m.now = func() time.Time { return clock }
	m.Apply(ctx, model.AgentClaude, ev(EventStop, ""), "", 0)
	clock = clock.Add(time.Minute)
	m.Sweep(ctx, func(int, time.Time) bool { return false })
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusWaitingInput {
		t.Fatalf("got %s", s.Status)
	}
	clock = clock.Add(UnknownAfter)
	m.Sweep(ctx, func(int, time.Time) bool { return false })
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusUnknown {
		t.Fatalf("got %s", s.Status)
	}
}

// probeSession puts a session into the state CheckInterrupts looks at:
// running, with a transcript known from its last event.
func probeSession(t *testing.T, m *Manager, id, path, event string) {
	t.Helper()
	in := HookInput{SessionID: id, Cwd: "/Users/me/proj", HookEventName: event, TranscriptPath: path}
	if _, err := m.Apply(context.Background(), model.AgentClaude, in, "", 4242); err != nil {
		t.Fatal(err)
	}
}

// A running session whose transcript is gone is skipped, not a crash.
func TestCheckInterruptsMissingTranscript(t *testing.T) {
	m, _, _ := newManager(t)
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeTranscript(t, path, rowPrompt)
	probeSession(t, m, "s1", path, EventUserPromptSubmit)
	os.Remove(path)
	m.CheckInterrupts(context.Background())
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusRunning {
		t.Fatalf("status: %s", s.Status)
	}
}

// A transcript that grew is read again even when its mtime did not move
// (two writes within the clock's resolution).
func TestCheckInterruptsSameMtimeNewSize(t *testing.T) {
	m, _, _ := newManager(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeTranscript(t, path, rowPrompt)
	probeSession(t, m, "s1", path, EventUserPromptSubmit)
	appendTranscript(t, path, rowToolUse)
	m.CheckInterrupts(ctx) // remembers this size and mtime
	fi, _ := os.Stat(path)
	appendTranscript(t, path, rowRejected, rowInterrupt)
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	m.CheckInterrupts(ctx)
	if s, _ := m.Get("claude:s1"); s.Status != model.StatusWaitingInput {
		t.Fatalf("status: %s", s.Status)
	}
}

// Every session is checked, whatever the others look like: waiting ones
// are skipped and transcripts without a marker leave theirs alone.
func TestCheckInterruptsVisitsEverySession(t *testing.T) {
	m, _, _ := newManager(t)
	dir := t.TempDir()
	for i := 0; i < 12; i++ {
		waiting := filepath.Join(dir, fmt.Sprintf("w%d.jsonl", i))
		writeTranscript(t, waiting, rowPrompt)
		probeSession(t, m, fmt.Sprintf("w%d", i), waiting, EventStop)
		quiet := filepath.Join(dir, fmt.Sprintf("q%d.jsonl", i))
		writeTranscript(t, quiet, rowPrompt)
		probeSession(t, m, fmt.Sprintf("q%d", i), quiet, EventUserPromptSubmit)
		appendTranscript(t, quiet, rowToolUse)
	}
	target := filepath.Join(dir, "target.jsonl")
	writeTranscript(t, target, rowPrompt)
	probeSession(t, m, "target", target, EventUserPromptSubmit)
	appendTranscript(t, target, rowToolUse, rowRejected, rowInterrupt)
	m.CheckInterrupts(context.Background())
	if s, _ := m.Get("claude:target"); s.Status != model.StatusWaitingInput {
		t.Fatalf("target: %s", s.Status)
	}
	for i := 0; i < 12; i++ {
		if s, _ := m.Get(fmt.Sprintf("claude:q%d", i)); s.Status != model.StatusRunning {
			t.Fatalf("q%d: %s", i, s.Status)
		}
	}
}

// Apply keeps the transcript it knows: an event without a path leaves it,
// a new path replaces it, and its read offset follows the file's size
// (0 when the file was emptied, unchanged when it cannot be read).
func TestApplyTracksTranscript(t *testing.T) {
	m, _, _ := newManager(t)
	dir := t.TempDir()
	p1, p2 := filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")
	tr := func() *transcript {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.transcripts["claude:s1"]
	}
	apply := func(event, path string) {
		t.Helper()
		in := HookInput{SessionID: "s1", HookEventName: event, TranscriptPath: path}
		if _, err := m.Apply(context.Background(), model.AgentClaude, in, "", 4242); err != nil {
			t.Fatal(err)
		}
	}
	apply(EventSessionStart, "")
	if tr() != nil {
		t.Fatal("transcript without a path")
	}
	writeTranscript(t, p1, rowPrompt, rowToolUse)
	size, _ := os.Stat(p1)
	apply(EventUserPromptSubmit, p1)
	if x := tr(); x == nil || x.path != p1 || x.from != size.Size() {
		t.Fatalf("p1: %+v", x)
	}
	apply(EventPreToolUse, "")
	if x := tr(); x == nil || x.path != p1 {
		t.Fatalf("an event without a path replaced it: %+v", x)
	}
	os.Remove(p1)
	apply(EventPostToolUse, p1)
	if x := tr(); x.from != size.Size() {
		t.Fatalf("unreadable file moved the offset: %+v", x)
	}
	writeTranscript(t, p1)
	os.Truncate(p1, 0)
	apply(EventStop, p1)
	if x := tr(); x.from != 0 {
		t.Fatalf("emptied file: %+v", x)
	}
	writeTranscript(t, p2, rowPrompt)
	apply(EventUserPromptSubmit, p2)
	if x := tr(); x.path != p2 {
		t.Fatalf("new path: %+v", x)
	}
}
