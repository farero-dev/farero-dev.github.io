// Package session tracks agent sessions from hook events (아키텍처 6장 세션
// 상태 기계) and persists them through the store.
package session

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/store"
)

// HookInput is the subset of a Claude Code hook's stdin JSON farero uses.
type HookInput struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	PermissionMode string          `json:"permission_mode"`
	Source         string          `json:"source"` // SessionStart
	Reason         string          `json:"reason"` // SessionEnd
	Message        string          `json:"message"`
	// NotificationType is the kind of a Notification (permission_prompt,
	// idle_prompt, ...). Older Claude Code versions leave it out.
	NotificationType string `json:"notification_type"`
	// AgentID is set for events from a subagent (same session_id as the
	// parent). AgentType is its kind; Claude Code's internal forks leave the
	// key out entirely.
	AgentID   string  `json:"agent_id"`
	AgentType *string `json:"agent_type"`
}

// Fork reports an event from one of Claude Code's internal forks
// (compaction, prompt suggestions, background summaries): agent_id without
// agent_type. Their PreToolUse is for a tool that never runs (M2 실험,
// 2.1.290), so it says nothing about the session.
func (in HookInput) Fork() bool { return in.AgentID != "" && in.AgentType == nil }

// Hook event names (Claude Code).
const (
	EventSessionStart       = "SessionStart"
	EventUserPromptSubmit   = "UserPromptSubmit"
	EventPreToolUse         = "PreToolUse"
	EventPostToolUse        = "PostToolUse"
	EventPostToolUseFailure = "PostToolUseFailure"
	EventPermissionRequest  = "PermissionRequest"
	EventNotification       = "Notification"
	EventStop               = "Stop"
	EventStopFailure        = "StopFailure" // the turn ended on an API error (v2.1.78)
	EventSessionEnd         = "SessionEnd"
)

// SourceCompact is SessionStart's source after context compaction.
const SourceCompact = "compact"

// QuestionTools reach PermissionRequest but are questions for the user, not
// permissions: no approval card, and the session waits for input.
var QuestionTools = map[string]bool{"AskUserQuestion": true}

// Notification types (Claude Code 2.1.290). Others, such as auth_success
// or agent_needs_input (background sessions of `claude agents`), leave the
// session as it is.
const (
	notePermission        = "permission_prompt" // a permission prompt is open in the terminal
	noteIdle              = "idle_prompt"       // 60 s after Stop
	noteElicitation       = "elicitation_dialog"
	noteElicitationURL    = "elicitation_url_dialog"
	noteElicitationDone   = "elicitation_complete"
	noteElicitationAnswer = "elicitation_response"
)

// UnknownAfter is how long a silent session waits before it is shown as
// unknown when its agent process is not known (no PID).
const UnknownAfter = 10 * time.Minute

// ExitGrace is how long a session's agent process must have been gone before
// the session is shown as unknown, so a SessionEnd still on its way wins.
// Claude Code does not always send SessionEnd: /exit while a PermissionRequest
// hook waits sends none (M2 실험), so the process is the real signal.
const ExitGrace = 5 * time.Second

// Manager owns the in-memory session table backed by the store.
type Manager struct {
	mu       sync.Mutex
	store    *store.Store
	now      func() time.Time
	sessions map[string]*model.Session
	onUpdate func(model.Session)

	transcripts map[string]*transcript // by session id, memory only
	deadSince   map[string]time.Time   // when the agent process was first seen gone

	// pubMu orders publishing (store, then onUpdate). Changes happen under
	// mu, and every publisher sends the state as it is when its turn comes,
	// so the store and the app never end on an older state.
	pubMu sync.Mutex
}

// transcript is a session's transcript file. from is its size at the
// session's last event (Notification aside) and gen counts those events:
// only an interrupt marker written after from is about the current turn.
// size and mtime are what CheckInterrupts last read.
type transcript struct {
	path  string
	from  int64
	gen   uint64
	size  int64
	mtime time.Time
}

// NewManager loads existing sessions. onUpdate (may be nil) is called after
// every change, outside the lock.
func NewManager(ctx context.Context, st *store.Store, onUpdate func(model.Session)) (*Manager, error) {
	m := &Manager{store: st, now: time.Now, sessions: map[string]*model.Session{}, onUpdate: onUpdate,
		transcripts: map[string]*transcript{}, deadSince: map[string]time.Time{}}
	list, err := st.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		s := list[i]
		m.sessions[s.ID] = &s
	}
	return m, nil
}

// SetOnUpdate replaces the change callback.
func (m *Manager) SetOnUpdate(f func(model.Session)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onUpdate = f
}

// Apply runs one hook event through the state machine, creating the session
// the first time it is seen (even mid-session, e.g. a session started before
// farero was installed).
func (m *Manager) Apply(ctx context.Context, agent string, in HookInput, tty string, pid int) (model.Session, error) {
	now := m.now()
	id := model.SessionKey(agent, in.SessionID)
	if pid <= 1 {
		// A hook still running after claude exited belongs to launchd now.
		pid = 0
	}
	tsize := int64(-1)
	if in.TranscriptPath != "" && in.HookEventName != EventNotification {
		if fi, err := os.Stat(in.TranscriptPath); err == nil {
			tsize = fi.Size()
		}
	}
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		s = &model.Session{ID: id, Agent: agent, AgentSessionID: in.SessionID, StartedAt: now, LastEventAt: now, Status: model.StatusRunning}
		m.sessions[id] = s
	}
	// Hooks are separate processes and SessionEnd runs asynchronously, so
	// an event of the process that just ended the session can arrive after
	// SessionEnd. Only SessionStart or another process (`claude --resume`)
	// reopens an ended session.
	late := s.Status == model.StatusEnded && in.HookEventName != EventSessionStart && (pid <= 0 || pid == s.PID)
	if in.Cwd != "" {
		s.Cwd = in.Cwd
	}
	if tty != "" {
		s.TTY = tty
	}
	if pid > 0 {
		s.PID = pid
	}
	t := m.transcripts[id]
	if in.TranscriptPath != "" && (t == nil || t.path != in.TranscriptPath) {
		t = &transcript{path: in.TranscriptPath}
		m.transcripts[id] = t
	}
	if !late && !in.Fork() {
		s.LastEventAt = now
		transition(s, in, ok)
		if t != nil && in.HookEventName != EventNotification {
			t.gen++
			if tsize >= 0 {
				t.from = tsize
			}
		}
	}
	snap := *s
	m.mu.Unlock()

	if err := m.publish(ctx, id); err != nil {
		return snap, err
	}
	if err := m.store.AddHookEvent(ctx, id, now, in.HookEventName, in.ToolName); err != nil {
		return snap, err
	}
	return snap, nil
}

// publish stores a session's current state and tells onUpdate.
func (m *Manager) publish(ctx context.Context, id string) error {
	m.pubMu.Lock()
	defer m.pubMu.Unlock()
	m.mu.Lock()
	s, ok := m.sessions[id]
	var snap model.Session
	if ok {
		snap = *s
	}
	cb := m.onUpdate
	m.mu.Unlock()
	if !ok {
		return nil // deleted meanwhile
	}
	if err := m.store.UpsertSession(ctx, snap); err != nil {
		return err
	}
	if cb != nil {
		cb(snap)
	}
	return nil
}

// transition applies one event of the state machine (아키텍처 6장). seen is
// false for the session's first event.
func transition(s *model.Session, in HookInput, seen bool) {
	switch in.HookEventName {
	case EventSessionStart:
		// Compaction can run in the middle of a turn; the session goes on
		// as it was.
		if in.Source == SourceCompact && seen {
			return
		}
		s.Status = model.StatusWaitingInput
		s.CurrentTool = ""
	case EventUserPromptSubmit:
		s.Status = model.StatusRunning
	case EventPreToolUse:
		s.Status = model.StatusRunning
		s.CurrentTool = in.ToolName
	case EventPostToolUse, EventPostToolUseFailure:
		s.Status = model.StatusRunning
		s.CurrentTool = ""
	case EventPermissionRequest:
		s.Status = model.StatusWaitingApproval
		if QuestionTools[in.ToolName] {
			s.Status = model.StatusWaitingInput
		}
		s.CurrentTool = in.ToolName
	case EventNotification:
		switch in.NotificationType {
		case notePermission:
			s.Status = model.StatusWaitingApproval
		case noteIdle:
			// A background subagent's card can still be open.
			if s.Status != model.StatusWaitingApproval {
				s.Status = model.StatusWaitingInput
				s.CurrentTool = ""
			}
		case noteElicitation, noteElicitationURL:
			// An MCP server asks the user something while its tool runs.
			s.Status = model.StatusWaitingInput
		case noteElicitationDone, noteElicitationAnswer:
			s.Status = model.StatusRunning
		case "":
			// Older Claude Code: the message is either a permission prompt
			// or idle input.
			if s.Status != model.StatusWaitingApproval {
				s.Status = model.StatusWaitingInput
				s.CurrentTool = ""
			}
		}
	case EventStop, EventStopFailure:
		s.Status = model.StatusWaitingInput
		s.CurrentTool = ""
	case EventSessionEnd:
		s.Status = model.StatusEnded
		s.CurrentTool = ""
	}
}

// Get returns a session by farero id.
func (m *Manager) Get(id string) (model.Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return model.Session{}, false
	}
	return *s, true
}

// List returns all sessions, newest first.
func (m *Manager) List() []model.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// SetStatus changes a session's status (approval waits started by the
// gateway, and the return to running afterwards).
func (m *Manager) SetStatus(ctx context.Context, id, status string) {
	m.mutate(ctx, id, func(s *model.Session) bool {
		if s.Status == status || s.Status == model.StatusEnded {
			return false
		}
		s.Status = status
		return true
	})
}

// TurnStopped records that the user stopped the turn in the agent (no hook
// event says so): the session waits for input and runs no tool.
func (m *Manager) TurnStopped(ctx context.Context, id string) {
	m.mutate(ctx, id, func(s *model.Session) bool {
		if s.Status == model.StatusEnded || (s.Status == model.StatusWaitingInput && s.CurrentTool == "") {
			return false
		}
		s.Status = model.StatusWaitingInput
		s.CurrentTool = ""
		return true
	})
}

// MarkTainted flags a session as having read untrusted content. It stays
// tainted until the session is deleted (기능 명세서 6-2 step 5).
func (m *Manager) MarkTainted(ctx context.Context, id string) {
	m.mutate(ctx, id, func(s *model.Session) bool {
		if s.Tainted {
			return false
		}
		s.Tainted = true
		return true
	})
}

// mutate changes a session under the lock; f reports whether it changed
// anything.
func (m *Manager) mutate(ctx context.Context, id string, f func(*model.Session) bool) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	changed := ok && f(s)
	m.mu.Unlock()
	if changed {
		_ = m.publish(ctx, id)
	}
}

// Delete removes a session and its log.
func (m *Manager) Delete(ctx context.Context, id string) error {
	// No publish may store the session again after it is gone.
	m.pubMu.Lock()
	defer m.pubMu.Unlock()
	if err := m.store.DeleteSession(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.sessions, id)
	delete(m.transcripts, id)
	delete(m.deadSince, id)
	m.mu.Unlock()
	return nil
}

// Sweep marks sessions unknown whose agent process is gone (for ExitGrace),
// or, when the process is not known, that have been silent for
// UnknownAfter (아키텍처 16장). alive reports whether pid is a process that
// already existed at the given time (the session's last event), so a reused
// PID does not keep a session open. It returns the PIDs found dead.
func (m *Manager) Sweep(ctx context.Context, alive func(pid int, before time.Time) bool) []int {
	now := m.now()
	m.mu.Lock()
	var changed []string
	var dead []int
	for id, s := range m.sessions {
		if s.Status == model.StatusEnded || s.Status == model.StatusUnknown {
			delete(m.deadSince, id)
			continue
		}
		if s.PID > 0 {
			if alive(s.PID, s.LastEventAt) {
				delete(m.deadSince, id)
				continue
			}
			since, seen := m.deadSince[id]
			if !seen {
				m.deadSince[id] = now
				continue
			}
			if now.Sub(since) < ExitGrace {
				continue
			}
			dead = append(dead, s.PID)
		} else if now.Sub(s.LastEventAt) < UnknownAfter {
			continue
		}
		delete(m.deadSince, id)
		s.Status = model.StatusUnknown
		s.CurrentTool = ""
		changed = append(changed, id)
	}
	m.mu.Unlock()
	for _, id := range changed {
		_ = m.publish(ctx, id)
	}
	return dead
}

// CheckInterrupts finds turns the user stopped. Esc, Ctrl-C and "No" at
// the terminal's permission prompt send no hook event at all (M2 실험,
// 2.1.290); Claude Code only writes a marker into the transcript. A running
// (or approval-waiting) session whose transcript got that marker after its
// last event waits for input again.
func (m *Manager) CheckInterrupts(ctx context.Context) {
	type probe struct {
		id, path string
		from     int64
		gen      uint64
	}
	m.mu.Lock()
	var probes []probe
	for id, s := range m.sessions {
		if s.Status != model.StatusRunning && s.Status != model.StatusWaitingApproval {
			continue
		}
		if t := m.transcripts[id]; t != nil {
			probes = append(probes, probe{id, t.path, t.from, t.gen})
		}
	}
	m.mu.Unlock()

	for _, p := range probes {
		fi, err := os.Stat(p.path)
		if err != nil || fi.Size() <= p.from {
			continue
		}
		m.mu.Lock()
		t := m.transcripts[p.id]
		unchanged := t == nil || (t.size == fi.Size() && t.mtime.Equal(fi.ModTime()))
		if t != nil {
			t.size, t.mtime = fi.Size(), fi.ModTime()
		}
		m.mu.Unlock()
		if unchanged || !transcriptInterrupted(p.path, p.from) {
			continue
		}
		m.mutate(ctx, p.id, func(s *model.Session) bool {
			// An event that arrived meanwhile decides instead.
			if t := m.transcripts[p.id]; t == nil || t.gen != p.gen {
				return false
			}
			if s.Status != model.StatusRunning && s.Status != model.StatusWaitingApproval {
				return false
			}
			s.Status = model.StatusWaitingInput
			s.CurrentTool = ""
			return true
		})
	}
}
