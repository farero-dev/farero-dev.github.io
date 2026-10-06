package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/farero-dev/farero/daemon/internal/broker"
	"github.com/farero-dev/farero/daemon/internal/gateway"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/policy"
	"github.com/farero-dev/farero/daemon/internal/secret"
	"github.com/farero-dev/farero/daemon/internal/session"
)

// Serve dispatches a new IPC connection by its first message.
func (c *Core) Serve(ctx context.Context, conn *ipc.Conn, first ipc.Message) {
	switch first.Type {
	case ipc.TypeHookEvent:
		c.serveHook(ctx, conn, first)
	case ipc.TypeHeadersIssue:
		c.serveHeaders(ctx, conn, first)
	case ipc.TypeUIHello:
		c.serveUI(ctx, conn, first)
	default:
		conn.SendError(first.ID, fmt.Errorf("unexpected first message %q", first.Type))
	}
}

func (c *Core) serveHook(ctx context.Context, conn *ipc.Conn, m ipc.Message) {
	ev, err := ipc.Decode[ipc.HookEvent](m)
	if err != nil {
		conn.SendError(m.ID, err)
		return
	}
	var in session.HookInput
	if err := json.Unmarshal(ev.Input, &in); err != nil || in.SessionID == "" {
		conn.Send(ipc.TypeHookAck, m.ID, nil)
		return
	}
	agent := ev.Agent
	if agent == "" {
		agent = model.AgentClaude
	}
	s, err := c.sessions.Apply(ctx, agent, in, ev.TTY, ev.PID)
	if err != nil {
		c.log.Error("session update", "err", err)
	}
	if in.Fork() {
		// Internal forks name tools they never run (session.HookInput.Fork).
		conn.Send(ipc.TypeHookAck, m.ID, nil)
		return
	}
	c.settleOnEvent(s.ID, in)
	c.trackToolUse(ctx, s.ID, in)
	switch in.HookEventName {
	case session.EventPreToolUse:
		if name, ok := strings.CutPrefix(in.ToolName, GatewayPrefix); ok {
			c.corr.Expect(s.ID, ev.PID, name, in.ToolInput)
		}
	case session.EventSessionEnd:
		c.policy.EndSession(s.ID)
	case session.EventPermissionRequest:
		if session.QuestionTools[in.ToolName] || strings.HasPrefix(in.ToolName, GatewayPrefix) {
			break
		}
		// The hook process disappears when the agent stops waiting (the
		// user answered in the terminal, or the turn was interrupted).
		hctx, cancel := context.WithCancel(ctx)
		defer cancel()
		w := c.addWait(s.ID, in, cancel)
		defer c.removeWait(w)
		go func() {
			for {
				if _, err := conn.Read(); err != nil {
					if !errors.Is(err, io.EOF) {
						c.log.Debug("hook read", "err", err)
					}
					cancel()
					return
				}
			}
		}()
		d := c.decideAgentTool(hctx, s, in, w)
		conn.Send(ipc.TypeHookDecision, m.ID, d)
		return
	}
	conn.Send(ipc.TypeHookAck, m.ID, nil)
}

// decideAgentTool handles a PermissionRequest (F-04). w is its wait: settled
// once the agent went on without farero's answer.
func (c *Core) decideAgentTool(ctx context.Context, s model.Session, in session.HookInput, w *agentWait) ipc.HookDecision {
	start := time.Now()
	logCtx := context.WithoutCancel(ctx)
	input := in.ToolInput
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	call := model.Call{SessionID: s.ID, TS: start, Kind: model.KindAgent, Agent: s.Agent, Tool: in.ToolName, Input: input}
	finish := func(decision, reason string, out ipc.HookDecision) ipc.HookDecision {
		call.Decision = decision
		call.Reason = reason
		call.DurationMS = time.Since(start).Milliseconds()
		call.ID = c.logCall(logCtx, call)
		if out.Behavior != ipc.BehaviorNone {
			c.sessions.SetStatus(logCtx, s.ID, model.StatusRunning)
		}
		return out
	}
	// refuse logs a deny that the agent ignores if the user already allowed
	// in the terminal; watchRefusal corrects the row when the tool runs.
	refuse := func(decision, msg string) ipc.HookDecision {
		out := finish(decision, "", ipc.HookDecision{Behavior: ipc.BehaviorDeny, Reason: msg})
		c.watchRefusal(logCtx, w, call.ID)
		return out
	}

	d := c.policy.DecideAgent(policy.AgentCall{
		SessionID: s.ID, Tool: in.ToolName, Input: input,
		Tainted: s.Tainted, UIConnected: c.broker.UIConnected(),
	})
	switch d.Outcome {
	case policy.Run:
		return finish(d.Decision, joinReasons(d.Reasons), ipc.HookDecision{Behavior: ipc.BehaviorAllow})
	case policy.Passthrough:
		return finish(d.Decision, joinReasons(d.Reasons), ipc.HookDecision{Behavior: ipc.BehaviorNone})
	}

	res := c.broker.Request(ctx, model.Approval{
		Kind: model.KindAgent, SessionID: s.ID, SessionLabel: sessionLabel(s), Agent: s.Agent,
		Tool: in.ToolName, Input: input, Reasons: d.Reasons, AllowSession: d.AllowSession,
	})
	if res.Outcome != broker.UIGone && w.settled.Load() {
		// The agent went on before farero's answer reached it (the user
		// answered in the terminal). A session grant still counts.
		if res.Outcome == broker.Answered && res.Answer == model.AnswerAllowSession {
			c.policy.GrantAgent(s.ID, in.ToolName, input)
		}
		return finish(model.DecisionCancelled, model.ReasonAnsweredInAgent, ipc.HookDecision{Behavior: ipc.BehaviorNone})
	}
	switch res.Outcome {
	case broker.Answered:
		switch res.Answer {
		case model.AnswerAllowSession:
			c.policy.GrantAgent(s.ID, in.ToolName, input)
			return finish(model.DecisionUserAllowed, model.AnswerAllowSession, ipc.HookDecision{Behavior: ipc.BehaviorAllow})
		case model.AnswerAllow:
			return finish(model.DecisionUserAllowed, "", ipc.HookDecision{Behavior: ipc.BehaviorAllow})
		default:
			return refuse(model.DecisionDenied, "사용자가 거부함")
		}
	case broker.TimedOut:
		return refuse(model.DecisionTimeout, "승인 대기 시간 초과")
	case broker.UIGone:
		return finish(model.DecisionPassthrough, model.ReasonAppNotRunning, ipc.HookDecision{Behavior: ipc.BehaviorNone})
	default:
		// The hook was stopped: the user denied or pressed Esc at the
		// terminal prompt, which sends no hook event (M2 실험), or the
		// session is ending (SessionEnd wins over this). A background
		// subagent's prompt is not in the terminal while its hook runs, so
		// its hook ends only with the session.
		if in.AgentID == "" {
			c.sessions.TurnStopped(context.WithoutCancel(ctx), s.ID)
		}
		return finish(model.DecisionCancelled, "", ipc.HookDecision{Behavior: ipc.BehaviorNone})
	}
}

// serveHeaders issues gateway headers to the headersHelper: the shared
// bearer secret and a fresh connection id tied to the agent's PID.
func (c *Core) serveHeaders(_ context.Context, conn *ipc.Conn, m ipc.Message) {
	req, err := ipc.Decode[ipc.HeadersIssue](m)
	if err != nil {
		conn.SendError(m.ID, err)
		return
	}
	sec, err := c.secrets.Get(secret.KeyGatewaySecret)
	if err != nil {
		conn.SendError(m.ID, err)
		return
	}
	connID := secret.RandomToken()[:16]
	c.corr.SetConnPID(connID, req.PID)
	conn.Send(ipc.TypeHeadersResult, m.ID, ipc.HeadersResult{Headers: map[string]string{
		"Authorization":    "Bearer " + sec,
		gateway.ConnHeader: connID,
	}})
}
