package correlate

import (
	"encoding/json"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTest() (*Correlator, *clock) {
	c := New()
	clk := &clock{t: time.Unix(1_000_000, 0)}
	c.now = clk.now
	return c, clk
}

func TestMatchIgnoresKeyOrder(t *testing.T) {
	c, _ := newTest()
	// Hook input keeps the model's key order; the MCP request sorts keys.
	c.Expect("claude:s1", 100, "dev_echo", json.RawMessage(`{"text":"한글","count":3,"opts":{"zeta":1,"alpha":[2,1]}}`))
	sid, m := c.Match("conn1", "dev_echo", json.RawMessage(`{"count":3,"opts":{"alpha":[2,1],"zeta":1},"text":"한글"}`))
	if sid != "claude:s1" || m != ByHook {
		t.Fatalf("got %q %q", sid, m)
	}
	// The entry is consumed.
	if sid, _ := c.Match("conn1", "dev_echo", json.RawMessage(`{"text":"한글","count":3,"opts":{"zeta":1,"alpha":[2,1]}}`)); sid != "" {
		t.Fatalf("entry reused: %q", sid)
	}
}

func TestArrayOrderMatters(t *testing.T) {
	c, _ := newTest()
	c.Expect("claude:s1", 1, "t", json.RawMessage(`{"a":[1,2]}`))
	if sid, _ := c.Match("", "t", json.RawMessage(`{"a":[2,1]}`)); sid != "" {
		t.Fatal("different arrays must not match")
	}
}

func TestExpiry(t *testing.T) {
	c, clk := newTest()
	c.Expect("claude:s1", 1, "t", json.RawMessage(`{}`))
	clk.t = clk.t.Add(TTL + time.Second)
	if sid, _ := c.Match("", "t", json.RawMessage(`{}`)); sid != "" {
		t.Fatal("expired entry matched")
	}
}

func TestAmbiguousResolvedByPID(t *testing.T) {
	c, _ := newTest()
	in := json.RawMessage(`{"repo":"x"}`)
	c.Expect("claude:a", 100, "github_list_issues", in)
	c.Expect("claude:b", 200, "github_list_issues", in)
	if sid, _ := c.Match("unknown-conn", "github_list_issues", in); sid != "" {
		t.Fatalf("ambiguous match without PID must be unknown, got %q", sid)
	}
	c.SetConnPID("connB", 200)
	sid, m := c.Match("connB", "github_list_issues", in)
	if sid != "claude:b" || m != ByPID {
		t.Fatalf("got %q %q", sid, m)
	}
	// Only a's entry remains, so it now matches uniquely.
	if sid, m := c.Match("other", "github_list_issues", in); sid != "claude:a" || m != ByHook {
		t.Fatalf("remaining entry: %q %q", sid, m)
	}
}

func TestSameSessionTwiceIsNotAmbiguous(t *testing.T) {
	c, _ := newTest()
	in := json.RawMessage(`{"q":1}`)
	c.Expect("claude:a", 1, "t", in)
	c.Expect("claude:a", 1, "t", in)
	for i := 0; i < 2; i++ {
		if sid, _ := c.Match("", "t", in); sid != "claude:a" {
			t.Fatalf("call %d: %q", i, sid)
		}
	}
}

func TestNoHookIsUnknown(t *testing.T) {
	c, _ := newTest()
	c.SetConnPID("conn", 100)
	if sid, _ := c.Match("conn", "t", json.RawMessage(`{}`)); sid != "" {
		t.Fatalf("a call without a PreToolUse must be unknown, got %q", sid)
	}
}
