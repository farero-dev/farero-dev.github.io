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

// An app update ships a new table; the user's overrides stay in the DB
// and are applied to it (6-5).
func TestOverridesSurviveTableUpdate(t *testing.T) {
	v1, err := ParseTable([]byte(`{"version": 1, "plugins": {"dev": {"tools": {
	  "write":  {"level": "ask"},
	  "read":   {"level": "auto"},
	  "send":   {"level": "ask"},
	  "deploy": {"level": "ask"}
	}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	e1 := NewEngine(v1, nil)
	for tool, level := range map[string]string{"write": LevelBlock, "read": LevelAsk, "send": LevelAuto, "deploy": LevelAuto} {
		if err := e1.SetOverride("dev", tool, level); err != nil {
			t.Fatal(err)
		}
	}
	stored := e1.Overrides() // what the DB holds

	v2, err := ParseTable([]byte(`{"version": 2, "plugins": {"dev": {"tools": {
	  "write":  {"level": "auto"},
	  "read":   {"level": "ask"},
	  "send":   {"level": "ask", "no_session": true},
	  "deploy": {"level": "ask"}
	}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	e2 := NewEngine(v2, stored)
	look := func(tool string) Effective {
		eff, _ := e2.Lookup("dev", tool)
		return eff
	}
	// The default changed; the user's level stays.
	if eff := look("write"); eff.Level != LevelBlock || !eff.Overridden || eff.DefaultLevel != LevelAuto {
		t.Errorf("write: %+v", eff)
	}
	// The new default happens to equal the user's level: still the user's.
	if eff := look("read"); eff.Level != LevelAsk || !eff.Overridden {
		t.Errorf("read: %+v", eff)
	}
	// Unchanged rule keeps the override.
	if eff := look("deploy"); eff.Level != LevelAuto || !eff.Overridden {
		t.Errorf("deploy: %+v", eff)
	}
	// The tool became no_session: the auto override no longer applies (Q33).
	if eff := look("send"); eff.Level != LevelAsk || eff.Overridden {
		t.Errorf("send: %+v", eff)
	}
	d := e2.DecidePlugin(PluginCall{Plugin: "dev", Tool: "send", SessionID: "claude:s1", UIConnected: true})
	if d.Outcome != Ask || d.AllowSession || d.Decision == model.DecisionAutoAllowed {
		t.Errorf("send decision: %+v", d)
	}
	if d := e2.DecidePlugin(PluginCall{Plugin: "dev", Tool: "send", SessionID: "claude:s1"}); d.Outcome != Deny {
		t.Errorf("send without UI: %+v", d)
	}
	if got := e2.IgnoredOverrides(); !slices.Equal(got, []string{"dev_send=auto"}) {
		t.Errorf("ignored: %v", got)
	}
	// The row is kept: a later table without no_session applies it again.
	if e2.Overrides()["dev_send"] != LevelAuto {
		t.Fatalf("ignored override dropped: %v", e2.Overrides())
	}
	v3, _ := ParseTable([]byte(`{"version": 3, "plugins": {"dev": {"tools": {"send": {"level": "ask"}}}}}`))
	if eff, _ := NewEngine(v3, e2.Overrides()).Lookup("dev", "send"); eff.Level != LevelAuto || !eff.Overridden {
		t.Errorf("send after no_session dropped: %+v", eff)
	}
	// Choosing the default level resets, including an ignored override.
	if err := e2.SetOverride("dev", "read", LevelAsk); err != nil {
		t.Fatal(err)
	}
	if err := e2.SetOverride("dev", "send", LevelAsk); err != nil {
		t.Fatal(err)
	}
	if over := e2.Overrides(); over["dev_read"] != "" || over["dev_send"] != "" {
		t.Fatalf("reset to default kept a row: %v", over)
	}
	if eff := look("read"); eff.Overridden {
		t.Errorf("read after reset: %+v", eff)
	}
	if len(e2.IgnoredOverrides()) != 0 {
		t.Errorf("ignored after reset: %v", e2.IgnoredOverrides())
	}
}

// Every agent tool that edits a file is scoped by its path (Q38); Bash by
// its command. A different path or command needs its own grant.
func TestGrantKeyAgentScopesByPath(t *testing.T) {
	for _, tc := range []struct {
		tool, a, b, other string
	}{
		{"Write", `{"file_path":"/a.go","content":"1"}`, `{"file_path":"/a.go","content":"2"}`, `{"file_path":"/b.go","content":"1"}`},
		{"MultiEdit", `{"file_path":"/a.go","edits":[1]}`, `{"file_path":"/a.go","edits":[2]}`, `{"file_path":"/b.go","edits":[1]}`},
		{"NotebookEdit", `{"notebook_path":"/n.ipynb","new_source":"x"}`, `{"notebook_path":"/n.ipynb","new_source":"y"}`, `{"notebook_path":"/m.ipynb","new_source":"x"}`},
		{"Bash", `{"command":"make","description":"a"}`, `{"command":"make","description":"b"}`, `{"command":"make deploy","description":"a"}`},
	} {
		a := GrantKeyAgent(tc.tool, json.RawMessage(tc.a))
		if b := GrantKeyAgent(tc.tool, json.RawMessage(tc.b)); a != b {
			t.Errorf("%s: same target, different keys %q %q", tc.tool, a, b)
		}
		if o := GrantKeyAgent(tc.tool, json.RawMessage(tc.other)); a == o {
			t.Errorf("%s: other target shares the key %q", tc.tool, a)
		}
	}
	// The same path under another tool is another grant.
	if GrantKeyAgent("Write", json.RawMessage(`{"file_path":"/a.go"}`)) == GrantKeyAgent("Edit", json.RawMessage(`{"file_path":"/a.go"}`)) {
		t.Error("Write and Edit share a key")
	}
}

// Tools lists every classified tool, sorted by plugin, then tool.
func TestToolsSorted(t *testing.T) {
	tab := DefaultTable()
	tab.AddPlugin("aaa", map[string]Rule{"zeta": {Level: LevelAsk}, "alpha": {Level: LevelAuto}})
	list := NewEngine(tab, nil).Tools()
	if len(list) < 3 || list[0].Plugin != "aaa" || list[0].Tool != "alpha" || list[1].Tool != "zeta" {
		t.Fatalf("first entries: %+v", list[:min(3, len(list))])
	}
	for i := 1; i < len(list); i++ {
		p, q := list[i-1], list[i]
		if p.Plugin > q.Plugin || (p.Plugin == q.Plugin && p.Tool >= q.Tool) {
			t.Fatalf("not sorted at %d: %s/%s then %s/%s", i, p.Plugin, p.Tool, q.Plugin, q.Tool)
		}
	}
}

// AddPlugin works on a table without plugins too.
func TestAddPluginToEmptyTable(t *testing.T) {
	var tab Table
	tab.AddPlugin("dev", map[string]Rule{"echo": {Level: LevelAuto}})
	if eff, ok := NewEngine(&tab, nil).Lookup("dev", "echo"); !ok || eff.Level != LevelAuto {
		t.Fatalf("lookup: %+v %v", eff, ok)
	}
}

// Railway's bucket credentials are asked for (they are secrets) but only
// read: the annotation says so (F-05, Q36).
func TestAskedReadToolIsReadOnly(t *testing.T) {
	eff, ok := NewEngine(DefaultTable(), nil).Lookup("railway", "get-bucket-credentials")
	if !ok || eff.Level != LevelAsk || !eff.IsReadOnly(eff.DefaultLevel) {
		t.Fatalf("%+v %v", eff, ok)
	}
}
