package core

import (
	"context"
	"time"

	"github.com/farero-dev/farero/daemon/internal/broker"
	"github.com/farero-dev/farero/daemon/internal/gateway"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/policy"
	"github.com/farero-dev/farero/daemon/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Refusal messages returned to the model as tool errors (기능 명세서 5-4).
const (
	msgDenied        = "사용자가 거부함 (farero)"
	msgAppNotRunning = "farero 앱이 실행 중이 아니라 승인할 수 없음"
	msgTimeout       = "승인 대기 시간 초과 (farero)"
	msgBlocked       = "farero 정책으로 차단된 도구"
	msgCancelled     = "요청이 취소됨"
	msgNotConnected  = "플러그인이 연결되어 있지 않음 (farero)"
)

// CallTool implements gateway.Caller: correlate, decide, ask, call upstream,
// taint and log.
func (c *Core) CallTool(ctx context.Context, gc gateway.Call) *mcp.CallToolResult {
	start := time.Now()
	sessionID, _ := c.corr.Match(gc.ConnID, gc.Name, gc.Args)
	var s model.Session
	known := false
	if sessionID != "" {
		s, known = c.sessions.Get(sessionID)
		if !known {
			sessionID = ""
		}
	}
	call := model.Call{SessionID: sessionID, ConnID: gc.ConnID, TS: start, Kind: model.KindPlugin,
		Agent: model.AgentClaude, Tool: gc.Name, Input: gc.Args}
	if known {
		call.Agent = s.Agent
	}
	logCtx := context.WithoutCancel(ctx)
	finish := func(decision, reason, errText string, res *mcp.CallToolResult) *mcp.CallToolResult {
		call.Decision = decision
		call.Reason = reason
		call.Error = errText
		call.ResultText = upstream.ResultText(res)
		call.DurationMS = time.Since(start).Milliseconds()
		c.logCall(logCtx, call)
		return res
	}

	t, ok := c.lookupExposed(gc.Name)
	if !ok {
		return finish(model.DecisionBlocked, model.ReasonUnclassified, "", upstream.ErrorResult(msgBlocked))
	}
	call.Plugin, call.Tool = t.plugin, t.tool

	d := c.policy.DecidePlugin(policy.PluginCall{
		Plugin: t.plugin, Tool: t.tool, SessionID: sessionID,
		Tainted: known && s.Tainted, UIConnected: c.broker.UIConnected(),
	})
	decision := d.Decision
	reason := joinReasons(d.Reasons)
	switch d.Outcome {
	case policy.Block:
		return finish(decision, reason, "", upstream.ErrorResult(msgBlocked))
	case policy.Deny:
		return finish(decision, reason, "", upstream.ErrorResult(msgAppNotRunning))
	case policy.Ask:
		if known {
			c.sessions.SetStatus(logCtx, sessionID, model.StatusWaitingApproval)
		}
		res := c.broker.Request(ctx, model.Approval{
			Kind: model.KindPlugin, SessionID: sessionID, SessionLabel: labelOrUnknown(s, known),
			Agent: call.Agent, Plugin: t.plugin, Tool: t.tool, Input: gc.Args,
			Reasons: d.Reasons, AllowSession: d.AllowSession,
		})
		if known {
			c.sessions.SetStatus(logCtx, sessionID, model.StatusRunning)
		}
		switch {
		case res.Outcome == broker.Answered && res.Answer == model.AnswerAllowSession:
			c.policy.GrantPlugin(sessionID, t.plugin, t.tool)
			decision, reason = model.DecisionUserAllowed, model.AnswerAllowSession
		case res.Outcome == broker.Answered && res.Answer == model.AnswerAllow:
			decision = model.DecisionUserAllowed
		case res.Outcome == broker.Answered:
			return finish(model.DecisionDenied, reason, "", upstream.ErrorResult(msgDenied))
		case res.Outcome == broker.TimedOut:
			return finish(model.DecisionTimeout, reason, "", upstream.ErrorResult(msgTimeout))
		case res.Outcome == broker.UIGone:
			return finish(model.DecisionAutoDenied, model.ReasonAppNotRunning, "", upstream.ErrorResult(msgAppNotRunning))
		default:
			return finish(model.DecisionCancelled, reason, "", upstream.ErrorResult(msgCancelled))
		}
	}

	p, ok := c.plugins.Get(t.plugin)
	if !ok {
		return finish(decision, reason, "plugin not connected", upstream.ErrorResult(msgNotConnected))
	}
	res, err := p.CallTool(ctx, t.tool, gc.Args)
	if err != nil {
		return finish(decision, reason, err.Error(), upstream.ErrorResult("업스트림 호출 실패: "+err.Error()))
	}
	// 6-2 step 5: reading untrusted content taints the session after the
	// result is returned. An unknown session is already treated as tainted.
	if eff, ok := c.policy.Lookup(t.plugin, t.tool); ok && eff.Taint && known && !res.IsError {
		c.sessions.MarkTainted(logCtx, sessionID)
	}
	errText := ""
	if res.IsError {
		errText = "upstream returned an error"
	}
	return finish(decision, reason, errText, res)
}

func labelOrUnknown(s model.Session, known bool) string {
	if !known {
		return "세션 불명"
	}
	return sessionLabel(s)
}
