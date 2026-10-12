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
	sid, m := c.Match(NewConnID(200, "b"), "github_list_issues", in)
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
	if sid, _ := c.Match(NewConnID(100, "x"), "t", json.RawMessage(`{}`)); sid != "" {
		t.Fatalf("a call without a PreToolUse must be unknown, got %q", sid)
	}
}

// The connection id carries the agent PID, so the tie-break needs no
// memory: Claude Code keeps its id across a farerod restart without running
// the headersHelper again (M4).
func TestConnIDCarriesPID(t *testing.T) {
	for conn, want := range map[string]int{
		NewConnID(4242, "abc"): 4242,
		NewConnID(0, "abc"):    0, // unknown agent process
		NewConnID(1, "abc"):    0, // launchd, not an agent
		"abc":                  0,
		"x.abc":                0,
		"":                     0,
	} {
		if got := connPID(conn); got != want {
			t.Errorf("connPID(%q) = %d, want %d", conn, got, want)
		}
	}
	c, _ := newTest()
	in := json.RawMessage(`{}`)
	c.Expect("claude:a", 100, "dev_echo", in)
	c.Expect("claude:b", 200, "dev_echo", in)
	if sid, m := New().Match(NewConnID(100, "x"), "dev_echo", in); sid != "" || m != None {
		t.Fatalf("no pending entry: %q %q", sid, m)
	}
	if sid, m := c.Match(NewConnID(0, "x"), "dev_echo", in); sid != "" {
		t.Fatalf("ambiguous match without a PID must be unknown, got %q %q", sid, m)
	}
	if sid, m := c.Match(NewConnID(100, "x"), "dev_echo", in); sid != "claude:a" || m != ByPID {
		t.Fatalf("got %q %q", sid, m)
	}
}
