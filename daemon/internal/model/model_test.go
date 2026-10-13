package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSessionKey(t *testing.T) {
	if got := SessionKey(AgentClaude, "abc-123"); got != "claude:abc-123" {
		t.Fatalf("SessionKey = %q", got)
	}
	if got := SessionKey("", ""); got != ":" {
		t.Fatalf("SessionKey empty = %q", got)
	}
}

// The JSON field names are the IPC contract the Swift app decodes.
func TestJSONFieldNames(t *testing.T) {
	cases := map[string]struct {
		v    any
		keys []string
	}{
		"session":  {Session{}, []string{"id", "agent", "agent_session_id", "cwd", "tty", "pid", "status", "current_tool", "tainted", "started_at", "last_event_at"}},
		"call":     {Call{}, []string{"id", "session_id", "conn_id", "ts", "kind", "agent", "plugin", "tool", "input", "result_text", "result_bytes", "decision", "reason", "duration_ms", "error"}},
		"approval": {Approval{}, []string{"id", "kind", "session_id", "session_label", "agent", "plugin", "tool", "input", "reasons", "allow_session", "created_at", "deadline"}},
		"plugin":   {PluginState{Plugin: "p"}, []string{"plugin", "status", "account_label"}},
	}
	for name, c := range cases {
		b, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, k := range c.keys {
			if _, ok := m[k]; !ok {
				t.Errorf("%s: missing key %q in %s", name, k, b)
			}
		}
		if len(m) != len(c.keys) {
			t.Errorf("%s: %d keys, want %d: %s", name, len(m), len(c.keys), b)
		}
	}
}

func TestPluginStateOmitEmpty(t *testing.T) {
	b, _ := json.Marshal(PluginState{Plugin: "p", Status: PluginConnected})
	var m map[string]any
	json.Unmarshal(b, &m)
	for _, k := range []string{"connected_at", "error", "options"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s should be omitted: %s", k, b)
		}
	}
	b, _ = json.Marshal(PluginState{ConnectedAt: time.Unix(1, 0), Error: "x", Options: map[string]string{"a": "b"}})
	m = nil
	json.Unmarshal(b, &m)
	for _, k := range []string{"connected_at", "error", "options"} {
		if _, ok := m[k]; !ok {
			t.Errorf("%s should be present: %s", k, b)
		}
	}
}

// Wire values of the constants are part of the contract too.
func TestConstantValues(t *testing.T) {
	for got, want := range map[string]string{
		AgentClaude: "claude", AgentCodex: "codex",
		StatusRunning: "running", StatusWaitingInput: "waiting_input", StatusWaitingApproval: "waiting_approval", StatusEnded: "ended", StatusUnknown: "unknown",
		KindPlugin: "plugin", KindAgent: "agent",
		DecisionAutoAllowed: "auto_allowed", DecisionUserAllowed: "user_allowed", DecisionSessionAllowed: "session_allowed",
		DecisionDenied: "denied", DecisionAutoDenied: "auto_denied", DecisionTimeout: "timeout", DecisionBlocked: "blocked",
		DecisionPassthrough: "passthrough", DecisionCancelled: "cancelled",
		ReasonPolicy: "policy", ReasonTainted: "tainted", ReasonUnknownSession: "unknown_session", ReasonAppNotRunning: "app_not_running",
		ReasonUnclassified: "unclassified", ReasonAgentRequest: "agent_request", ReasonAnsweredInAgent: "answered_in_agent",
		AnswerAllow: "allow", AnswerAllowSession: "allow_session", AnswerDeny: "deny",
		PluginDisconnected: "disconnected", PluginConnected: "connected", PluginExpired: "expired", PluginError: "error",
	} {
		if got != want {
			t.Errorf("constant = %q, want %q", got, want)
		}
	}
}
