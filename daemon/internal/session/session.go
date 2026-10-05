// Package session tracks agent sessions from hook events (아키텍처 6장 세션
// 상태 기계) and persists them through the store.
package session

import (
	"context"
	"encoding/json"
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
}

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
	EventSessionEnd         = "SessionEnd"
)

// UnknownAfter is how long a silent session with no live process waits
// before it is shown as unknown (아키텍처 16장, 제안).
const UnknownAfter = 10 * time.Minute

// Manager owns the in-memory session table backed by the store.
type Manager struct {
	mu       sync.Mutex
	store    *store.Store
	now      func() time.Time
	sessions map[string]*model.Session
	onUpdate func(model.Session)
}

// NewManager loads existing sessions. onUpdate (may be nil) is called after
// every change, outside the lock.
func NewManager(ctx context.Context, st *store.Store, onUpdate func(model.Session)) (*Manager, error) {
	m := &Manager{store: st, now: time.Now, sessions: map[string]*model.Session{}, onUpdate: onUpdate}
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
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		s = &model.Session{ID: id, Agent: agent, AgentSessionID: in.SessionID, StartedAt: now, Status: model.StatusRunning}
		m.sessions[id] = s
	}
	if in.Cwd != "" {
		s.Cwd = in.Cwd
	}
	if tty != "" {
		s.TTY = tty
	}
	if pid > 0 {
		s.PID = pid
	}
	s.LastEventAt = now
	switch in.HookEventName {
	case EventSessionStart:
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
		s.CurrentTool = in.ToolName
	case EventNotification, EventStop:
		s.Status = model.StatusWaitingInput
		s.CurrentTool = ""
	case EventSessionEnd:
		s.Status = model.StatusEnded
		s.CurrentTool = ""
	}
	snap := *s
	cb := m.onUpdate
	m.mu.Unlock()

	if err := m.store.UpsertSession(ctx, snap); err != nil {
		return snap, err
	}
	if err := m.store.AddHookEvent(ctx, id, now, in.HookEventName, in.ToolName); err != nil {
		return snap, err
	}
	if cb != nil {
		cb(snap)
	}
	return snap, nil
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

func (m *Manager) mutate(ctx context.Context, id string, f func(*model.Session) bool) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok || !f(s) {
		m.mu.Unlock()
		return
	}
	snap := *s
	cb := m.onUpdate
	m.mu.Unlock()
	_ = m.store.UpsertSession(ctx, snap)
	if cb != nil {
		cb(snap)
	}
}

// Delete removes a session and its log.
func (m *Manager) Delete(ctx context.Context, id string) error {
	if err := m.store.DeleteSession(ctx, id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	return nil
}

// Sweep marks sessions unknown when they have been silent for UnknownAfter
// and their agent process is gone. It returns the PIDs found dead.
func (m *Manager) Sweep(ctx context.Context, alive func(pid int) bool) []int {
	cutoff := m.now().Add(-UnknownAfter)
	m.mu.Lock()
	var changed []model.Session
	var dead []int
	for _, s := range m.sessions {
		if s.Status == model.StatusEnded || s.Status == model.StatusUnknown {
			continue
		}
		if s.LastEventAt.After(cutoff) {
			continue
		}
		if s.PID > 0 && alive(s.PID) {
			continue
		}
		s.Status = model.StatusUnknown
		s.CurrentTool = ""
		changed = append(changed, *s)
		if s.PID > 0 {
			dead = append(dead, s.PID)
		}
	}
	cb := m.onUpdate
	m.mu.Unlock()
	for _, s := range changed {
		_ = m.store.UpsertSession(ctx, s)
		if cb != nil {
			cb(s)
		}
	}
	return dead
}
