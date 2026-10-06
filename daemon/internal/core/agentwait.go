package core

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/farero-dev/farero/daemon/internal/policy"
	"github.com/farero-dev/farero/daemon/internal/session"
)

// agentWait is a PermissionRequest whose hook waits for the app's answer.
//
// Claude Code shows its own permission prompt in the terminal at the same
// time (M0). When the user allows there, the hook is not stopped: the tool
// runs and PostToolUse arrives while farero still waits (M2 실험, 2.1.290).
// Those events settle the wait.
type agentWait struct {
	session string
	agentID string // "" for the main agent
	tool    string
	input   string // policy.CanonicalHash of tool_input
	cancel  context.CancelFunc
	settled atomic.Bool // the agent went on without farero's answer
}

type agentWaits struct {
	mu sync.Mutex
	m  map[*agentWait]struct{}
}

func (c *Core) addWait(sessionID string, in session.HookInput, cancel context.CancelFunc) *agentWait {
	w := &agentWait{session: sessionID, agentID: in.AgentID, tool: in.ToolName, input: policy.CanonicalHash(in.ToolInput), cancel: cancel}
	c.waits.mu.Lock()
	if c.waits.m == nil {
		c.waits.m = map[*agentWait]struct{}{}
	}
	c.waits.m[w] = struct{}{}
	c.waits.mu.Unlock()
	return w
}

func (c *Core) removeWait(w *agentWait) {
	c.waits.mu.Lock()
	delete(c.waits.m, w)
	c.waits.mu.Unlock()
}

// settleWaits ends the waits of a session that match: the card goes away
// and the hook answers nothing.
func (c *Core) settleWaits(sessionID string, match func(*agentWait) bool) {
	c.waits.mu.Lock()
	var hit []*agentWait
	for w := range c.waits.m {
		if w.session == sessionID && match(w) {
			hit = append(hit, w)
		}
	}
	c.waits.mu.Unlock()
	for _, w := range hit {
		w.settled.Store(true)
		w.cancel()
	}
}

// settleOnEvent settles the waits an agent event shows were answered
// elsewhere.
func (c *Core) settleOnEvent(sessionID string, in session.HookInput) {
	switch in.HookEventName {
	case session.EventPostToolUse, session.EventPostToolUseFailure:
		// The tool ran (or failed) after all.
		hash := policy.CanonicalHash(in.ToolInput)
		c.settleWaits(sessionID, func(w *agentWait) bool {
			return w.agentID == in.AgentID && w.tool == in.ToolName && w.input == hash
		})
	case session.EventStop, session.EventStopFailure, session.EventUserPromptSubmit:
		// The main agent's turn moved on. A background subagent can still
		// be asking after the main agent stopped.
		if in.AgentID == "" {
			c.settleWaits(sessionID, func(w *agentWait) bool { return w.agentID == "" })
		}
	case session.EventSessionEnd:
		c.settleWaits(sessionID, func(*agentWait) bool { return true })
	}
}
