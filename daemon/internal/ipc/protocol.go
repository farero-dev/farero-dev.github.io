// Package ipc is the local socket protocol between farerod and its clients
// (farero-hook and the app). Messages are JSON Lines: one object per line
// with "type", an optional "id" that replies echo back, and "data".
// docs/ipc.md documents every message.
package ipc

import (
	"encoding/json"
	"time"

	"github.com/farero-dev/farero/daemon/internal/model"
)

// Message is one line on the socket.
type Message struct {
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Message types.
const (
	// hook client
	TypeHookEvent    = "hook.event"
	TypeHookAck      = "hook.ack"
	TypeHookDecision = "hook.decision"

	// headers client (headersHelper)
	TypeHeadersIssue  = "headers.issue"
	TypeHeadersResult = "headers.result"

	// ui client
	TypeUIHello           = "ui.hello"
	TypeStateSnapshot     = "state.snapshot"
	TypeSessionUpdated    = "session.updated"
	TypeSessionRemoved    = "session.removed"
	TypeApprovalRequest   = "approval.request"
	TypeApprovalResponse  = "approval.response"
	TypeApprovalCancelled = "approval.cancelled"
	TypeCallLogged        = "call.logged"
	TypeGatewayStatus     = "gateway.status"
	TypePluginUpdated     = "plugin.updated"
	TypeLogQuery          = "log.query"
	TypeLogResult         = "log.result"
	TypePolicyGet         = "policy.get"
	TypePolicySet         = "policy.set"
	TypePolicyState       = "policy.state"
	TypeSessionDelete     = "session.delete"
	TypePluginList        = "plugin.list"
	TypePluginConnect     = "plugin.connect"
	TypePluginDisconnect  = "plugin.disconnect"
	TypePluginSetOption   = "plugin.set_option"
	TypePluginPrompt      = "plugin.prompt" // device code / URL the user must act on
	TypeAgentCfgStatus    = "agentcfg.status"
	TypeAgentCfgPlan      = "agentcfg.plan"
	TypeAgentCfgApply     = "agentcfg.apply"
	TypeAgentCfgRemove    = "agentcfg.remove"
	TypeSettingsGet       = "settings.get"
	TypeSettingsSet       = "settings.set"

	// generic replies
	TypeOK    = "ok"
	TypeError = "error"
)

// HookEvent is sent by farero-hook for every agent hook invocation.
type HookEvent struct {
	Agent string          `json:"agent"`
	Input json.RawMessage `json:"input"` // the hook's stdin JSON, unmodified
	TTY   string          `json:"tty"`
	PID   int             `json:"pid"` // agent process id
}

// Hook decision behaviors.
const (
	BehaviorAllow = "allow"
	BehaviorDeny  = "deny"
	BehaviorNone  = "none" // no decision: the agent shows its own prompt
)

// HookDecision answers a PermissionRequest.
type HookDecision struct {
	Behavior string `json:"behavior"`
	Reason   string `json:"reason,omitempty"`
}

// HeadersIssue asks for gateway request headers.
type HeadersIssue struct {
	Agent string `json:"agent"`
	PID   int    `json:"pid"`
}

// HeadersResult carries the headers the headersHelper prints.
type HeadersResult struct {
	Headers map[string]string `json:"headers"`
}

// UIHello opens a UI connection.
type UIHello struct {
	Version string `json:"version"`
}

// GatewayInfo describes the MCP gateway listener.
type GatewayInfo struct {
	Running bool   `json:"running"`
	Port    int    `json:"port"`
	URL     string `json:"url"`
	Error   string `json:"error,omitempty"`
}

// StateSnapshot is the reply to ui.hello.
type StateSnapshot struct {
	Version   string              `json:"version"`
	Sessions  []model.Session     `json:"sessions"`
	Approvals []model.Approval    `json:"approvals"`
	Plugins   []model.PluginState `json:"plugins"`
	Gateway   GatewayInfo         `json:"gateway"`
}

// ApprovalResponse is the user's answer to an approval card.
type ApprovalResponse struct {
	ApprovalID string `json:"approval_id"`
	Answer     string `json:"answer"`
}

// ApprovalCancelled withdraws a card (timeout or the agent cancelled).
type ApprovalCancelled struct {
	ApprovalID string `json:"approval_id"`
	Reason     string `json:"reason"`
}

// SessionRef names one session.
type SessionRef struct {
	SessionID string `json:"session_id"`
}

// PolicySet changes one tool's level ("" resets to the default).
type PolicySet struct {
	Plugin string `json:"plugin"`
	Tool   string `json:"tool"`
	Level  string `json:"level"`
}

// PluginRef names a plugin.
type PluginRef struct {
	Plugin string `json:"plugin"`
}

// PluginConnect starts connecting a plugin. Params carries plugin-specific
// input (for Gmail the user's OAuth client id and secret).
type PluginConnect struct {
	Plugin string            `json:"plugin"`
	Params map[string]string `json:"params,omitempty"`
}

// PluginSetOption sets a plugin option (e.g. GitHub read_only).
type PluginSetOption struct {
	Plugin string `json:"plugin"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

// PluginPrompt asks the user to act during OAuth (enter a device code at a
// URL, or finish in the browser).
type PluginPrompt struct {
	Plugin    string    `json:"plugin"`
	UserCode  string    `json:"user_code,omitempty"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// Settings are user-adjustable daemon settings.
type Settings struct {
	ResultLimitBytes int  `json:"result_limit_bytes"`
	UpdateCheck      bool `json:"update_check"`
}

// ErrorData is the payload of an error reply.
type ErrorData struct {
	Message string `json:"message"`
}
