// Package correlate ties gateway MCP calls to agent sessions (기능 명세서
// 5-2, 아키텍처 7-1).
//
// MCP requests do not carry the agent's session id. Claude Code runs the
// PreToolUse hook for mcp__farero__<tool> right before sending the request
// (M0 experiment 1: 60–90 ms earlier), so the hook's (tool, input) is
// recorded here and matched against the call that follows. A pending entry
// lives for 30 seconds.
//
// When the same (tool, input) is pending for more than one session the match
// is ambiguous. The connection's agent PID (reported by the headersHelper,
// whose parent is the agent process, as is the hook's) is then used to pick
// the right one; if that does not settle it the call is "unknown session".
package correlate

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/farero-dev/farero/daemon/internal/policy"
)

// TTL is how long a PreToolUse entry waits for its MCP call (Q62).
const TTL = 30 * time.Second

type entry struct {
	sessionID string
	pid       int
	at        time.Time
}

// Correlator holds pending PreToolUse entries and connection identities.
type Correlator struct {
	mu      sync.Mutex
	now     func() time.Time
	pending map[string][]entry // key -> entries in arrival order
	connPID map[string]int     // X-Farero-Conn -> agent pid
}

// New returns an empty correlator.
func New() *Correlator {
	return &Correlator{
		now:     time.Now,
		pending: map[string][]entry{},
		connPID: map[string]int{},
	}
}

// Key builds the match key for an exposed gateway tool name and its input.
func Key(tool string, input json.RawMessage) string {
	return tool + "\x00" + policy.CanonicalHash(input)
}

// Expect records a PreToolUse for a gateway tool (name without the
// mcp__farero__ prefix).
func (c *Correlator) Expect(sessionID string, pid int, tool string, input json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := Key(tool, input)
	c.pending[k] = append(c.prune(c.pending[k]), entry{sessionID: sessionID, pid: pid, at: c.now()})
}

// SetConnPID remembers which agent process a gateway connection belongs to.
func (c *Correlator) SetConnPID(conn string, pid int) {
	if conn == "" || pid <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connPID[conn] = pid
}

// Match result methods.
const (
	ByHook = "hook" // unique PreToolUse match
	ByPID  = "pid"  // ambiguous hook match settled by the connection's PID
	None   = ""     // unknown session
)

// Match finds the session for a gateway call and consumes the pending entry.
// It returns "" when the call cannot be tied to a session.
func (c *Correlator) Match(conn, tool string, input json.RawMessage) (sessionID, method string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := Key(tool, input)
	list := c.prune(c.pending[k])
	c.pending[k] = list
	if len(list) == 0 {
		delete(c.pending, k)
		return "", None
	}
	sessions := map[string]bool{}
	for _, e := range list {
		sessions[e.sessionID] = true
	}
	pick := -1
	method = ByHook
	if len(sessions) == 1 {
		pick = 0
	} else if pid, ok := c.connPID[conn]; ok {
		// Prefer an entry recorded by the same agent process.
		for i, e := range list {
			if e.pid == pid {
				pick = i
				method = ByPID
				break
			}
		}
	}
	if pick < 0 {
		return "", None
	}
	sessionID = list[pick].sessionID
	list = append(list[:pick:pick], list[pick+1:]...)
	if len(list) == 0 {
		delete(c.pending, k)
	} else {
		c.pending[k] = list
	}
	return sessionID, method
}

// Forget drops the connections of an agent process that has exited.
func (c *Correlator) Forget(pid int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for conn, p := range c.connPID {
		if p == pid {
			delete(c.connPID, conn)
		}
	}
}

func (c *Correlator) prune(list []entry) []entry {
	cutoff := c.now().Add(-TTL)
	i := 0
	for i < len(list) && list[i].at.Before(cutoff) {
		i++
	}
	return list[i:]
}
