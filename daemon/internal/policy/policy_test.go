package policy

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/farero-dev/farero/daemon/internal/model"
)

const testTable = `{
  "version": 1,
  "plugins": {
    "dev": {"tools": {
      "read":    {"level": "auto"},
      "taint":   {"level": "auto", "taint": true},
      "write":   {"level": "ask"},
      "destroy": {"level": "ask", "no_session": true, "destructive": true},
      "hidden":  {"level": "block"}
    }}
  }
}`

func newEngine(t *testing.T) *Engine {
	t.Helper()
	tab, err := ParseTable([]byte(testTable))
	if err != nil {
		t.Fatal(err)
	}
	return NewEngine(tab, nil)
}

func TestEmbeddedTableMatchesRepoCopy(t *testing.T) {
	repo, err := os.ReadFile("../../../policy/default.json")
	if err != nil {
		t.Skip("repo policy file not available:", err)
	}
	if string(repo) != string(defaultTable) {
		t.Fatal("daemon/internal/policy/default.json is stale: run `go generate ./internal/policy`")
	}
	tab := DefaultTable()
	e := NewEngine(tab, nil)
	// Spot-check decisions fixed in the design docs.
	for _, c := range []struct{ plugin, tool, level string }{
		{"railway", "railway-agent", LevelBlock},
		{"railway", "redeploy", LevelAsk},
		{"github", "issue_write", LevelAsk},
		{"github", "delete_repository", LevelBlock},
		{"resend", "send-email", LevelAsk},
		{"gmail", "get_message", LevelAuto},
	} {
		eff, ok := e.Lookup(c.plugin, c.tool)
		if !ok || eff.Level != c.level {
			t.Errorf("%s/%s: got %+v ok=%v, want %s", c.plugin, c.tool, eff, ok, c.level)
		}
	}
	if eff, _ := e.Lookup("resend", "send-email"); !eff.NoSession {
		t.Error("send-email must not allow session grants")
	}
	if eff, _ := e.Lookup("github", "issue_read"); !eff.Taint {
		t.Error("issue_read must be a taint source")
	}
	if e.Exposed("resend", "create-api-key") {
		t.Error("unlisted Resend tools must not be exposed")
	}
}

// Claude Code names gateway tools mcp__farero__<plugin>_<tool>, and the
// Claude API accepts tool names of at most 64 characters from this set.
var claudeToolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func TestExposedNamesFitClaude(t *testing.T) {
	for p, pt := range DefaultTable().Plugins {
		for name, r := range pt.Tools {
			if r.Level == LevelBlock {
				continue
			}
			if full := "mcp__farero__" + ToolKey(p, name); !claudeToolName.MatchString(full) {
				t.Errorf("%s is not a valid Claude tool name", full)
			}
		}
	}
}

func TestDecidePluginOrder(t *testing.T) {
	e := newEngine(t)
	base := PluginCall{Plugin: "dev", SessionID: "claude:s1", UIConnected: true}
	call := func(tool string, mod func(*PluginCall)) Decision {
		c := base
		c.Tool = tool
		if mod != nil {
			mod(&c)
		}
		return e.DecidePlugin(c)
	}

	if d := call("unlisted", nil); d.Outcome != Block || d.Reasons[0] != model.ReasonUnclassified {
		t.Errorf("unclassified: %+v", d)
	}
	if d := call("hidden", nil); d.Outcome != Block {
		t.Errorf("block: %+v", d)
	}
	// auto runs even when the app is not running (scenario D).
	if d := call("read", func(c *PluginCall) { c.UIConnected = false }); d.Outcome != Run || d.Decision != model.DecisionAutoAllowed {
		t.Errorf("auto without UI: %+v", d)
	}
	if d := call("write", func(c *PluginCall) { c.UIConnected = false }); d.Outcome != Deny || d.Decision != model.DecisionAutoDenied {
		t.Errorf("ask without UI: %+v", d)
	}
	d := call("write", nil)
	if d.Outcome != Ask || !d.AllowSession || !slices.Equal(d.Reasons, []string{model.ReasonPolicy}) {
		t.Errorf("ask: %+v", d)
	}
	if d := call("destroy", nil); d.Outcome != Ask || d.AllowSession {
		t.Errorf("no_session must hide the session button: %+v", d)
	}
}

func TestSessionGrantTaintAndUnknown(t *testing.T) {
	e := newEngine(t)
	c := PluginCall{Plugin: "dev", Tool: "write", SessionID: "claude:s1", UIConnected: true}
	e.GrantPlugin("claude:s1", "dev", "write")
	if d := e.DecidePlugin(c); d.Outcome != Run || d.Decision != model.DecisionSessionAllowed {
		t.Fatalf("grant should run: %+v", d)
	}
	// Scenario C: a tainted session asks again despite the grant.
	tainted := c
	tainted.Tainted = true
	d := e.DecidePlugin(tainted)
	if d.Outcome != Ask || d.AllowSession || !slices.Contains(d.Reasons, model.ReasonTainted) {
		t.Fatalf("tainted: %+v", d)
	}
	// Other sessions are unaffected by s1's grant.
	other := c
	other.SessionID = "claude:s2"
	if d := e.DecidePlugin(other); d.Outcome != Ask {
		t.Fatalf("grant leaked across sessions: %+v", d)
	}
	// Unknown session: always ask, never offer a session grant.
	unknown := c
	unknown.SessionID = ""
	d = e.DecidePlugin(unknown)
	if d.Outcome != Ask || d.AllowSession || !slices.Contains(d.Reasons, model.ReasonUnknownSession) {
		t.Fatalf("unknown: %+v", d)
	}
	// Grants are ignored for no_session tools even if somehow recorded.
	e.GrantPlugin("claude:s1", "dev", "destroy")
	if d := e.DecidePlugin(PluginCall{Plugin: "dev", Tool: "destroy", SessionID: "claude:s1", UIConnected: true}); d.Outcome != Ask {
		t.Fatalf("no_session honoured a grant: %+v", d)
	}
	e.EndSession("claude:s1")
	if d := e.DecidePlugin(c); d.Outcome != Ask {
		t.Fatalf("grant survived session end: %+v", d)
	}
}

func TestOverrides(t *testing.T) {
	e := newEngine(t)
	if err := e.SetOverride("dev", "destroy", LevelAuto); err == nil {
		t.Fatal("no_session tool must not become auto")
	}
	if err := e.SetOverride("dev", "destroy", LevelBlock); err != nil {
		t.Fatal(err)
	}
	if e.Exposed("dev", "destroy") {
		t.Fatal("blocked override still exposed")
	}
	if err := e.SetOverride("dev", "write", LevelAuto); err != nil {
		t.Fatal(err)
	}
	if d := e.DecidePlugin(PluginCall{Plugin: "dev", Tool: "write", UIConnected: false}); d.Outcome != Run {
		t.Fatalf("override to auto: %+v", d)
	}
	eff, _ := e.Lookup("dev", "write")
	if !eff.Overridden || eff.DefaultLevel != LevelAsk || eff.IsReadOnly(eff.DefaultLevel) {
		t.Fatalf("effective: %+v", eff)
	}
	// Setting the default level again removes the override.
	e.SetOverride("dev", "write", LevelAsk)
	if _, ok := e.Overrides()["dev_write"]; ok {
		t.Fatal("override to default level should be dropped")
	}
	if err := e.SetOverride("dev", "nope", LevelAsk); err == nil {
		t.Fatal("unknown tool accepted")
	}
}

func TestDecideAgent(t *testing.T) {
	e := newEngine(t)
	in := json.RawMessage(`{"command":"npm install","description":"Install deps"}`)
	c := AgentCall{SessionID: "claude:s1", Tool: "Bash", Input: in, UIConnected: true}
	if d := e.DecideAgent(AgentCall{SessionID: "claude:s1", Tool: "Bash", Input: in}); d.Outcome != Passthrough {
		t.Fatalf("no UI should pass through: %+v", d)
	}
	if d := e.DecideAgent(c); d.Outcome != Ask || !d.AllowSession {
		t.Fatalf("first request: %+v", d)
	}
	e.GrantAgent("claude:s1", "Bash", in)
	// Same command with a different description is the same grant.
	same := c
	same.Input = json.RawMessage(`{"description":"again","command":"npm install"}`)
	if d := e.DecideAgent(same); d.Outcome != Run {
		t.Fatalf("same command: %+v", d)
	}
	diff := c
	diff.Input = json.RawMessage(`{"command":"npm install left-pad"}`)
	if d := e.DecideAgent(diff); d.Outcome != Ask {
		t.Fatalf("different command reused grant: %+v", d)
	}
	// Q39: taint voids agent grants too.
	tainted := same
	tainted.Tainted = true
	if d := e.DecideAgent(tainted); d.Outcome != Ask || d.AllowSession {
		t.Fatalf("tainted agent call: %+v", d)
	}
}

func TestGrantKeyAgentScopes(t *testing.T) {
	a := GrantKeyAgent("Edit", json.RawMessage(`{"file_path":"/a.go","old_string":"x","new_string":"y"}`))
	b := GrantKeyAgent("Edit", json.RawMessage(`{"file_path":"/a.go","old_string":"p","new_string":"q"}`))
	c := GrantKeyAgent("Edit", json.RawMessage(`{"file_path":"/b.go"}`))
	if a != b || a == c {
		t.Fatal("edit grants must be per file")
	}
	x := GrantKeyAgent("WebFetch", json.RawMessage(`{"url":"u","prompt":"p"}`))
	y := GrantKeyAgent("WebFetch", json.RawMessage(`{"prompt":"p","url":"u"}`))
	if x != y {
		t.Fatal("generic grants must ignore key order")
	}
}

func TestParseTableRejectsAutoNoSession(t *testing.T) {
	_, err := ParseTable([]byte(`{"plugins":{"x":{"tools":{"t":{"level":"auto","no_session":true}}}}}`))
	if err == nil {
		t.Fatal("expected error")
	}
}
