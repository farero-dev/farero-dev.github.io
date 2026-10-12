package core

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/farero-dev/farero/daemon/internal/model"
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
	useID   string // tool_use_id of the PreToolUse before it, "" if none was seen
	cancel  context.CancelFunc
	settled atomic.Bool // the agent went on without farero's answer
}

type agentWaits struct {
	mu sync.Mutex
	m  map[*agentWait]struct{}
}

func (c *Core) addWait(sessionID string, in session.HookInput, cancel context.CancelFunc) *agentWait {
	w := &agentWait{session: sessionID, agentID: in.AgentID, tool: in.ToolName, input: policy.CanonicalHash(in.ToolInput), cancel: cancel}
	w.useID = c.toolUseOf(sessionID, in)
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
			if w.useID != "" && in.ToolUseID != "" && w.useID != in.ToolUseID {
				return false // an earlier use of the same tool and input
			}
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

// toolUseKey names a tool use by what the agent asked for.
type toolUseKey struct {
	session, agentID, tool, input string // input is policy.CanonicalHash of tool_input
}

func toolUseKeyOf(sessionID string, in session.HookInput) toolUseKey {
	return toolUseKey{sessionID, in.AgentID, in.ToolName, policy.CanonicalHash(in.ToolInput)}
}

// toolUses ties farero's answers to the tool uses they were about.
//
// A PermissionRequest has no tool_use_id, but the PreToolUse just before it
// (same tool and input) has. When farero refuses (deny, timeout) after the
// user allowed in the terminal, Claude Code ignores the hook's late answer
// and the tool runs (M2 실험): a PostToolUse with that tool_use_id shows it,
// and the log row is corrected.
type toolUses struct {
	mu      sync.Mutex
	pending map[toolUseKey]toolUse // the last PreToolUse for each tool and input
	refused map[string]refusal     // tool_use_id -> the log row of farero's refusal
}

type toolUse struct {
	id string
	at time.Time
}

type refusal struct {
	call int64
	at   time.Time
}

// toolUseTTL bounds how long a PreToolUse whose tool never ran, or a
// refusal the agent followed, is remembered.
const toolUseTTL = time.Hour

func (u *toolUses) pruneLocked(now time.Time) {
	for k, p := range u.pending {
		if now.Sub(p.at) > toolUseTTL {
			delete(u.pending, k)
		}
	}
	for id, r := range u.refused {
		if now.Sub(r.at) > toolUseTTL {
			delete(u.refused, id)
		}
	}
}

// takeRefusal removes and returns the refusal of a tool use.
func (u *toolUses) takeRefusal(id string) (refusal, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	r, ok := u.refused[id]
	if ok {
		delete(u.refused, id)
	}
	return r, ok
}

// toolUseOf returns the tool_use_id of the PreToolUse a PermissionRequest
// follows.
func (c *Core) toolUseOf(sessionID string, in session.HookInput) string {
	c.uses.mu.Lock()
	defer c.uses.mu.Unlock()
	return c.uses.pending[toolUseKeyOf(sessionID, in)].id
}

// trackToolUse remembers PreToolUse tool_use_ids and checks a PostToolUse
// against farero's refusals.
func (c *Core) trackToolUse(ctx context.Context, sessionID string, in session.HookInput) {
	if in.ToolUseID == "" {
		return
	}
	u := &c.uses
	switch in.HookEventName {
	case session.EventPreToolUse:
		now := time.Now()
		u.mu.Lock()
		u.pruneLocked(now)
		if u.pending == nil {
			u.pending = map[toolUseKey]toolUse{}
		}
		u.pending[toolUseKeyOf(sessionID, in)] = toolUse{in.ToolUseID, now}
		u.mu.Unlock()
	case session.EventPostToolUse, session.EventPostToolUseFailure:
		k := toolUseKeyOf(sessionID, in)
		u.mu.Lock()
		if u.pending[k].id == in.ToolUseID {
			delete(u.pending, k)
		}
		u.mu.Unlock()
		if r, ok := u.takeRefusal(in.ToolUseID); ok {
			c.ranAnyway(ctx, r.call)
		}
	}
}

// watchRefusal records that farero refused w's tool use (log row call), so
// a PostToolUse showing the tool ran anyway corrects the row. If the agent
// already went on (settleOnEvent got there first), the row is corrected now.
func (c *Core) watchRefusal(ctx context.Context, w *agentWait, call int64) {
	if call == 0 {
		return
	}
	if w.useID != "" {
		now := time.Now()
		c.uses.mu.Lock()
		c.uses.pruneLocked(now)
		if c.uses.refused == nil {
			c.uses.refused = map[string]refusal{}
		}
		c.uses.refused[w.useID] = refusal{call, now}
		c.uses.mu.Unlock()
	}
	if !w.settled.Load() {
		return
	}
	if w.useID != "" {
		if _, ok := c.uses.takeRefusal(w.useID); !ok {
			return // the PostToolUse already corrected it
		}
	}
	c.ranAnyway(ctx, call)
}

// ranAnyway corrects the log row of a refusal the agent did not follow: the
// user had allowed the tool in the terminal first.
func (c *Core) ranAnyway(ctx context.Context, call int64) {
	if err := c.store.SetCallDecision(ctx, call, model.DecisionCancelled, model.ReasonAnsweredInAgent); err != nil {
		c.log.Error("audit log", "err", err)
	}
}
