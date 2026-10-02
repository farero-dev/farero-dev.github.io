// Package model holds the domain types shared by farerod's packages and sent
// over the local IPC socket. JSON field names are part of the IPC contract
// (docs/ipc.md) and are decoded by the Swift app.
package model

import (
	"encoding/json"
	"time"
)

// Agent kinds. MVP supports Claude Code only; the field stays so Codex can be
// added back later (Q58).
const (
	AgentClaude = "claude"
	AgentCodex  = "codex"
)

// Session statuses (기능 명세서 5-1).
const (
	StatusRunning         = "running"
	StatusWaitingInput    = "waiting_input"
	StatusWaitingApproval = "waiting_approval"
	StatusEnded           = "ended"
	StatusUnknown         = "unknown"
)

// Session is one agent session_id.
type Session struct {
	ID             string    `json:"id"` // "<agent>:<agent_session_id>"
	Agent          string    `json:"agent"`
	AgentSessionID string    `json:"agent_session_id"`
	Cwd            string    `json:"cwd"`
	TTY            string    `json:"tty"`
	PID            int       `json:"pid"`
	Status         string    `json:"status"`
	CurrentTool    string    `json:"current_tool"`
	Tainted        bool      `json:"tainted"`
	StartedAt      time.Time `json:"started_at"`
	LastEventAt    time.Time `json:"last_event_at"`
}

// SessionKey builds the farero session id for an agent session.
func SessionKey(agent, agentSessionID string) string { return agent + ":" + agentSessionID }

// Call kinds.
const (
	KindPlugin = "plugin" // gateway tool call
	KindAgent  = "agent"  // the agent's own tool (PermissionRequest)
)

// Decisions recorded in the audit log (기능 명세서 F-09).
const (
	DecisionAutoAllowed    = "auto_allowed"
	DecisionUserAllowed    = "user_allowed"
	DecisionSessionAllowed = "session_allowed"
	DecisionDenied         = "denied"
	DecisionAutoDenied     = "auto_denied"
	DecisionTimeout        = "timeout"
	DecisionBlocked        = "blocked"
	DecisionPassthrough    = "passthrough" // app not running: left to the agent's own prompt
	DecisionCancelled      = "cancelled"   // the agent withdrew the request
)

// Reasons: why a call was asked about or how it was decided.
const (
	ReasonPolicy         = "policy"          // 분류표
	ReasonTainted        = "tainted"         // 오염
	ReasonUnknownSession = "unknown_session" // 세션 불명
	ReasonAppNotRunning  = "app_not_running"
	ReasonUnclassified   = "unclassified"
	ReasonAgentRequest   = "agent_request" // the agent asked for its own tool
)

// Call is one audit log row.
type Call struct {
	ID          int64           `json:"id"`
	SessionID   string          `json:"session_id"` // "" when the session is unknown
	ConnID      string          `json:"conn_id"`
	TS          time.Time       `json:"ts"`
	Kind        string          `json:"kind"`
	Agent       string          `json:"agent"`
	Plugin      string          `json:"plugin"`
	Tool        string          `json:"tool"`
	Input       json.RawMessage `json:"input"`
	ResultText  string          `json:"result_text"`
	ResultBytes int             `json:"result_bytes"`
	Decision    string          `json:"decision"`
	Reason      string          `json:"reason"`
	DurationMS  int64           `json:"duration_ms"`
	Error       string          `json:"error"`
}

// Approval decisions sent by the UI.
const (
	AnswerAllow        = "allow"
	AnswerAllowSession = "allow_session"
	AnswerDeny         = "deny"
)

// Approval is a pending request shown as an approval card.
type Approval struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"` // KindAgent or KindPlugin
	SessionID    string          `json:"session_id"`
	SessionLabel string          `json:"session_label"`
	Agent        string          `json:"agent"`
	Plugin       string          `json:"plugin"`
	Tool         string          `json:"tool"`
	Input        json.RawMessage `json:"input"`
	Reasons      []string        `json:"reasons"`
	AllowSession bool            `json:"allow_session"` // show "allow for this session"
	CreatedAt    time.Time       `json:"created_at"`
	Deadline     time.Time       `json:"deadline"`
}

// Plugin statuses (F-07 공통).
const (
	PluginDisconnected = "disconnected"
	PluginConnected    = "connected"
	PluginExpired      = "expired"
	PluginError        = "error"
)

// PluginState is a plugin's connection state as shown in the app.
type PluginState struct {
	Plugin       string            `json:"plugin"`
	Status       string            `json:"status"`
	AccountLabel string            `json:"account_label"`
	ConnectedAt  time.Time         `json:"connected_at,omitzero"`
	Error        string            `json:"error,omitempty"`
	Options      map[string]string `json:"options,omitempty"`
}
