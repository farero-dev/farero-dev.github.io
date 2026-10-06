package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/store"
)

// hookRaw sends one hook event with the given fields and returns the reply.
func (h *harness) hookRaw(in map[string]any, pid int) ipc.Message {
	h.t.Helper()
	conn, err := ipc.Dial(h.sock, time.Second)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close()
	raw, _ := json.Marshal(in)
	conn.Send(ipc.TypeHookEvent, "h", ipc.HookEvent{Agent: "claude", Input: raw, PID: pid})
	reply, err := conn.Read()
	if err != nil {
		h.t.Fatal(err)
	}
	return reply
}

func (h *harness) status(id string) string {
	s, _ := h.core.sessions.Get(id)
	return s.Status
}

// Allowing in the terminal does not end the waiting PermissionRequest hook
// (M2 실험, 2.1.290): the tool runs and PostToolUse arrives while farero
// still shows the card. The card goes away and the hook gets no decision.
func TestTerminalAllowClosesCard(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	done := make(chan ipc.Message, 1)
	go func() {
		done <- h.hook("s1", "PermissionRequest", "Bash", map[string]any{"command": "npm install", "description": "install"}, 100)
	}()
	ap := u.approval()
	// Same input, other key order.
	h.hookRaw(map[string]any{"session_id": "s1", "hook_event_name": "PostToolUse", "tool_name": "Bash",
		"tool_input": json.RawMessage(`{"description":"install","command":"npm install"}`)}, 100)
	c, _ := ipc.Decode[ipc.ApprovalCancelled](u.next(ipc.TypeApprovalCancelled))
	if c.ApprovalID != ap.ID {
		t.Fatalf("cancelled: %+v", c)
	}
	d, _ := ipc.Decode[ipc.HookDecision](<-done)
	if d.Behavior != ipc.BehaviorNone {
		t.Fatalf("decision: %+v", d)
	}
	if call := h.lastCall(); call.Decision != model.DecisionCancelled || call.Reason != model.ReasonAnsweredInAgent {
		t.Fatalf("log: %+v", call)
	}
	if st := h.status("claude:s1"); st != model.StatusRunning {
		t.Fatalf("status: %s", st)
	}
}

// Another tool's PostToolUse leaves the card alone.
func TestOtherToolKeepsCard(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	go h.hook("s1", "PermissionRequest", "Bash", map[string]any{"command": "npm install"}, 100)
	ap := u.approval()
	h.hook("s1", "PostToolUse", "Bash", map[string]any{"command": "ls"}, 100)
	h.hook("s2", "Stop", "", nil, 200)
	if p := h.core.broker.Pending(); len(p) != 1 || p[0].ID != ap.ID {
		t.Fatalf("pending: %+v", p)
	}
	u.answer(ap.ID, model.AnswerAllow)
}

// The turn ended (Stop) while a card of the main agent was open: it was
// settled in the agent.
func TestStopClosesMainAgentCard(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	done := make(chan ipc.Message, 1)
	go func() {
		done <- h.hook("s1", "PermissionRequest", "Bash", map[string]any{"command": "npm install"}, 100)
	}()
	ap := u.approval()
	h.hook("s1", "Stop", "", nil, 100)
	if c, _ := ipc.Decode[ipc.ApprovalCancelled](u.next(ipc.TypeApprovalCancelled)); c.ApprovalID != ap.ID {
		t.Fatalf("cancelled: %+v", c)
	}
	<-done
	if st := h.status("claude:s1"); st != model.StatusWaitingInput {
		t.Fatalf("status: %s", st)
	}
}

// A background subagent asks after the main agent's Stop; the main agent's
// events do not settle it.
func TestStopKeepsSubagentCard(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	go h.hookRaw(map[string]any{"session_id": "s1", "hook_event_name": "PermissionRequest", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "touch x"}, "agent_id": "abd87a99", "agent_type": "general-purpose"}, 100)
	ap := u.approval()
	h.hook("s1", "Stop", "", nil, 100)
	if p := h.core.broker.Pending(); len(p) != 1 || p[0].ID != ap.ID {
		t.Fatalf("pending: %+v", p)
	}
	u.answer(ap.ID, model.AnswerAllow)
}

// Denying or pressing Esc at the terminal prompt kills the waiting hook and
// sends no event: the session waits for input.
func TestHookGoneWaitsForInput(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	conn, err := ipc.Dial(h.sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	conn.Send(ipc.TypeHookEvent, "h", ipc.HookEvent{Agent: "claude", Input: raw, PID: 1})
	u.approval()
	conn.Close()
	u.next(ipc.TypeApprovalCancelled)
	waitFor(t, func() bool { return h.status("claude:s1") == model.StatusWaitingInput })
	if s, _ := h.core.sessions.Get("claude:s1"); s.CurrentTool != "" {
		t.Fatalf("current tool left: %q", s.CurrentTool)
	}
	if call := h.lastCall(); call.Decision != model.DecisionCancelled || call.Reason != "" {
		t.Fatalf("log: %+v", call)
	}
}

// A fork's PreToolUse names a gateway tool it never calls; it must not tie
// a later call to the session.
func TestForkPreToolUseIsNotExpected(t *testing.T) {
	h := newHarness(t, time.Minute)
	a := h.agent("s1", 100)
	args := map[string]any{"text": "x"}
	h.hookRaw(map[string]any{"session_id": "s1", "hook_event_name": "PreToolUse", "tool_name": GatewayPrefix + "dev_echo",
		"tool_input": args, "agent_id": "a47080ec2a632bdbe"}, 100)
	a.callNoHook("dev_echo", args)
	if c := h.lastCall(); c.SessionID != "" {
		t.Fatalf("tied to a session by a fork event: %+v", c)
	}
}

// A background subagent's hook ends when the session goes away; the
// subagent tells nothing about whether the main agent waits for input.
func TestSubagentHookGoneLeavesStatus(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	conn, err := ipc.Dial(h.sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "PermissionRequest", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "touch x"}, "agent_id": "abd87a99", "agent_type": "general-purpose"})
	conn.Send(ipc.TypeHookEvent, "h", ipc.HookEvent{Agent: "claude", Input: raw, PID: 100})
	u.approval()
	conn.Close()
	u.next(ipc.TypeApprovalCancelled)
	h.lastCall()
	if st := h.status("claude:s1"); st != model.StatusWaitingApproval {
		t.Fatalf("status: %s", st)
	}
}

func bashEvent(event, useID, command string) map[string]any {
	in := map[string]any{"session_id": "s1", "hook_event_name": event, "tool_name": "Bash",
		"tool_input": map[string]any{"command": command, "description": "run it"}}
	if useID != "" {
		in["tool_use_id"] = useID
	}
	return in
}

// askBash runs PreToolUse and a PermissionRequest for command and returns
// the card and the hook's pending reply.
func (h *harness) askBash(u *fakeUI, useID, command string) (model.Approval, chan ipc.Message) {
	h.t.Helper()
	h.hookRaw(bashEvent("PreToolUse", useID, command), 100)
	done := make(chan ipc.Message, 1)
	go func() { done <- h.hookRaw(bashEvent("PermissionRequest", "", command), 100) }()
	return u.approval(), done
}

func (h *harness) calls() []model.Call {
	h.t.Helper()
	calls, err := h.st.QueryCalls(context.Background(), store.CallFilter{Limit: 100})
	if err != nil {
		h.t.Fatal(err)
	}
	return calls
}

// The user allowed in the terminal, then denied on the card while the tool
// ran: Claude Code ignores the hook's late deny (M2 실험), so once the
// tool's PostToolUse arrives the log says the terminal answered.
func TestTerminalAllowBeforeAppDeny(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	ap, done := h.askBash(u, "toolu_1", "make build")
	u.answer(ap.ID, model.AnswerDeny)
	if d, _ := ipc.Decode[ipc.HookDecision](<-done); d.Behavior != ipc.BehaviorDeny {
		t.Fatalf("decision: %+v", d)
	}
	if c := h.lastCall(); c.Decision != model.DecisionDenied {
		t.Fatalf("log before PostToolUse: %+v", c)
	}
	h.hookRaw(bashEvent("PostToolUse", "toolu_1", "make build"), 100)
	waitFor(t, func() bool {
		c := h.calls()[0]
		return c.Decision == model.DecisionCancelled && c.Reason == model.ReasonAnsweredInAgent
	})
}

// After a deny the model may run the same command again: that is a new
// tool use, and the first row stays a deny.
func TestRetryAfterDenyKeepsDeny(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	ap, done := h.askBash(u, "toolu_1", "make build")
	u.answer(ap.ID, model.AnswerDeny)
	<-done
	ap, done = h.askBash(u, "toolu_2", "make build")
	u.answer(ap.ID, model.AnswerAllow)
	<-done
	h.hookRaw(bashEvent("PostToolUse", "toolu_2", "make build"), 100)
	h.hook("s1", "Stop", "", nil, 100) // processed after the PostToolUse
	calls := h.calls()
	if len(calls) != 2 || calls[0].Decision != model.DecisionUserAllowed || calls[1].Decision != model.DecisionDenied {
		t.Fatalf("log: %+v", calls)
	}
}

// A timeout is a refusal too: if the terminal had allowed, the tool ran.
func TestTerminalAllowBeforeTimeout(t *testing.T) {
	h := newHarness(t, 300*time.Millisecond)
	u := h.ui()
	_, done := h.askBash(u, "toolu_1", "sleep 1")
	if d, _ := ipc.Decode[ipc.HookDecision](<-done); d.Behavior != ipc.BehaviorDeny {
		t.Fatalf("decision: %+v", d)
	}
	if c := h.lastCall(); c.Decision != model.DecisionTimeout {
		t.Fatalf("log: %+v", c)
	}
	h.hookRaw(bashEvent("PostToolUseFailure", "toolu_1", "sleep 1"), 100)
	waitFor(t, func() bool { return h.calls()[0].Decision == model.DecisionCancelled })
}

// The PostToolUse can arrive between the app's answer and its log row: the
// row is corrected once, by whichever side comes second.
func TestRefusalSettledBeforeRegistered(t *testing.T) {
	h := newHarness(t, time.Minute)
	ctx := context.Background()
	id, err := h.st.InsertCall(ctx, model.Call{SessionID: "", TS: time.Now(), Kind: model.KindAgent, Tool: "Bash",
		Input: json.RawMessage(`{}`), Decision: model.DecisionDenied})
	if err != nil {
		t.Fatal(err)
	}
	w := &agentWait{useID: "toolu_9"}
	w.settled.Store(true)
	h.core.watchRefusal(ctx, w, id)
	if c := h.calls()[0]; c.Decision != model.DecisionCancelled || c.Reason != model.ReasonAnsweredInAgent {
		t.Fatalf("log: %+v", c)
	}
	if _, ok := h.core.uses.takeRefusal("toolu_9"); ok {
		t.Fatal("refusal left behind")
	}
}

// A PostToolUse of an earlier use of the same command does not settle a
// newer card.
func TestEarlierToolUseKeepsCard(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	ap, done := h.askBash(u, "toolu_2", "npm test")
	h.hookRaw(bashEvent("PostToolUse", "toolu_1", "npm test"), 100)
	if p := h.core.broker.Pending(); len(p) != 1 || p[0].ID != ap.ID {
		t.Fatalf("pending: %+v", p)
	}
	u.answer(ap.ID, model.AnswerAllow)
	if d, _ := ipc.Decode[ipc.HookDecision](<-done); d.Behavior != ipc.BehaviorAllow {
		t.Fatalf("decision: %+v", d)
	}
}
