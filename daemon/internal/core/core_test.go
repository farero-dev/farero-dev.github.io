package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/gateway"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/policy"
	"github.com/farero-dev/farero/daemon/internal/secret"
	"github.com/farero-dev/farero/daemon/internal/store"
	"github.com/farero-dev/farero/daemon/internal/upstream"
	"github.com/farero-dev/farero/daemon/internal/upstream/devplugin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type harness struct {
	t    *testing.T
	core *Core
	sock string
	st   *store.Store
}

func newHarness(t *testing.T, approvalTimeout time.Duration) *harness {
	t.Helper()
	return startHarness(t, memStore(t), devTable(), approvalTimeout, nil)
}

func memStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// devTable is the shipped table plus the dev plugin's rules.
func devTable() *policy.Table {
	tab := policy.DefaultTable()
	tab.AddPlugin(devplugin.Name, devplugin.Rules)
	return tab
}

// startHarness runs a Core on st, like farerod starting on an existing
// database. log may be nil.
func startHarness(t *testing.T, st *store.Store, tab *policy.Table, approvalTimeout time.Duration, log *slog.Logger) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c, err := New(ctx, Options{Version: "test", Store: st, Secrets: secret.NewMemoryStore(), Table: tab, Log: log, ApprovalTimeout: approvalTimeout})
	if err != nil {
		t.Fatal(err)
	}
	c.Plugins().Set(devplugin.New())
	c.RefreshTools(ctx)
	if err := c.StartGateway(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// A test that fails while a gateway call waits on a card must fail
		// now, not after the approval timeout: the HTTP server's shutdown
		// waits for that call.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		c.Shutdown(ctx)
	})

	dir, _ := os.MkdirTemp("/tmp", "frc")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := ipc.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	go ipc.Serve(ctx, l, c.Serve)
	return &harness{t: t, core: c, sock: sock, st: st}
}

// --- fake app ---

type fakeUI struct {
	t    *testing.T
	conn *ipc.Conn
	msgs chan ipc.Message
	snap ipc.StateSnapshot
}

func (h *harness) ui() *fakeUI {
	h.t.Helper()
	conn, err := ipc.Dial(h.sock, time.Second)
	if err != nil {
		h.t.Fatal(err)
	}
	conn.Send(ipc.TypeUIHello, "hello", ipc.UIHello{Version: "test"})
	first, err := conn.Read()
	if err != nil || first.Type != ipc.TypeStateSnapshot {
		h.t.Fatalf("snapshot: %v %+v", err, first)
	}
	snap, _ := ipc.Decode[ipc.StateSnapshot](first)
	u := &fakeUI{t: h.t, conn: conn, msgs: make(chan ipc.Message, 100), snap: snap}
	go func() {
		for {
			m, err := conn.Read()
			if err != nil {
				close(u.msgs)
				return
			}
			u.msgs <- m
		}
	}()
	waitFor(h.t, func() bool { return h.core.broker.UIConnected() })
	return u
}

// waitTimeout bounds how long the fake app waits for a message. The tests
// with inputs over 1 MB take a few seconds under -race on GitHub's runners.
const waitTimeout = 15 * time.Second

func (u *fakeUI) next(typ string) ipc.Message {
	u.t.Helper()
	timeout := time.After(waitTimeout)
	for {
		select {
		case m, ok := <-u.msgs:
			if !ok {
				u.t.Fatalf("ui closed waiting for %s", typ)
			}
			if m.Type == typ {
				return m
			}
		case <-timeout:
			u.t.Fatalf("timed out waiting for %s", typ)
		}
	}
}

// reply waits for the reply to request id, whatever its type.
func (u *fakeUI) reply(id string) ipc.Message {
	u.t.Helper()
	timeout := time.After(waitTimeout)
	for {
		select {
		case m, ok := <-u.msgs:
			if !ok {
				u.t.Fatalf("ui closed waiting for reply %s", id)
			}
			if m.ID == id {
				return m
			}
		case <-timeout:
			u.t.Fatalf("timed out waiting for reply %s", id)
		}
	}
}

func (u *fakeUI) approval() model.Approval {
	u.t.Helper()
	a, _ := ipc.Decode[model.Approval](u.next(ipc.TypeApprovalRequest))
	return a
}

func (u *fakeUI) answer(id, answer string) {
	u.t.Helper()
	u.conn.Send(ipc.TypeApprovalResponse, "r-"+id, ipc.ApprovalResponse{ApprovalID: id, Answer: answer})
}

func (u *fakeUI) close() { u.conn.Close() }

// --- fake hook ---

func (h *harness) hook(sessionID, event, tool string, input any, pid int) ipc.Message {
	h.t.Helper()
	conn, err := ipc.Dial(h.sock, time.Second)
	if err != nil {
		h.t.Fatal(err)
	}
	defer conn.Close()
	in := map[string]any{"session_id": sessionID, "cwd": "/Users/me/proj-" + sessionID, "hook_event_name": event}
	if tool != "" {
		in["tool_name"] = tool
		in["tool_input"] = input
	}
	raw, _ := json.Marshal(in)
	conn.Send(ipc.TypeHookEvent, "h", ipc.HookEvent{Agent: "claude", Input: raw, TTY: "ttys001", PID: pid})
	reply, err := conn.Read()
	if err != nil {
		h.t.Fatal(err)
	}
	return reply
}

// --- MCP agent ---

type headerRT map[string]string

func (hr headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range hr {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

type agent struct {
	h   *harness
	cs  *mcp.ClientSession
	sid string
	pid int
}

// agent connects like Claude Code: headers from headers.issue, then MCP.
func (h *harness) agent(sessionID string, pid int) *agent {
	h.t.Helper()
	conn, err := ipc.Dial(h.sock, time.Second)
	if err != nil {
		h.t.Fatal(err)
	}
	conn.Send(ipc.TypeHeadersIssue, "x", ipc.HeadersIssue{Agent: "claude", PID: pid})
	reply, err := conn.Read()
	conn.Close()
	if err != nil {
		h.t.Fatal(err)
	}
	hr, _ := ipc.Decode[ipc.HeadersResult](reply)
	cl := mcp.NewClient(&mcp.Implementation{Name: "claude-code", Version: "test"}, nil)
	cs, err := cl.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   gateway.URL(h.core.GatewayInfo().Port),
		HTTPClient: &http.Client{Transport: headerRT(hr.Headers)},
	}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() {
		// Close waits for calls in flight; one still waiting on a card
		// would hold a failed test until the approval timeout.
		done := make(chan struct{})
		go func() { cs.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})
	h.hook(sessionID, "SessionStart", "", nil, pid)
	return &agent{h: h, cs: cs, sid: sessionID, pid: pid}
}

// call runs PreToolUse then the MCP call, like Claude Code does.
func (a *agent) call(tool string, args map[string]any) *mcp.CallToolResult {
	a.h.t.Helper()
	a.h.hook(a.sid, "PreToolUse", GatewayPrefix+tool, args, a.pid)
	return a.callNoHook(tool, args)
}

func (a *agent) callNoHook(tool string, args map[string]any) *mcp.CallToolResult {
	a.h.t.Helper()
	res, err := a.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		a.h.t.Fatal(err)
	}
	return res
}

// callAsync runs call in the background.
func (a *agent) callAsync(tool string, args map[string]any) chan *mcp.CallToolResult {
	a.h.hook(a.sid, "PreToolUse", GatewayPrefix+tool, args, a.pid)
	ch := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, err := a.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			res = &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
		}
		ch <- res
	}()
	return ch
}

func text(r *mcp.CallToolResult) string {
	if r == nil || len(r.Content) == 0 {
		return ""
	}
	if t, ok := r.Content[0].(*mcp.TextContent); ok {
		return t.Text
	}
	return ""
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) lastCall() model.Call {
	h.t.Helper()
	var calls []model.Call
	waitFor(h.t, func() bool {
		calls, _ = h.st.QueryCalls(context.Background(), store.CallFilter{Limit: 1})
		return len(calls) > 0
	})
	return calls[0]
}

// --- tests: MVP 구현 순서 M4 완료 기준 ---

func TestToolListHidesBlockedAndUnclassified(t *testing.T) {
	h := newHarness(t, time.Minute)
	a := h.agent("s1", 100)
	lt, err := a.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range lt.Tools {
		names = append(names, x.Name)
		if x.Name == "dev_destroy_sim" && (x.Annotations == nil || x.Annotations.DestructiveHint == nil || !*x.Annotations.DestructiveHint) {
			t.Errorf("destroy_sim must be annotated destructive: %+v", x.Annotations)
		}
		if x.Name == "dev_echo" && !x.Annotations.ReadOnlyHint {
			t.Errorf("echo must be read-only")
		}
	}
	for _, hidden := range []string{"dev_blocked_sim", "dev_unclassified_sim"} {
		if slices.Contains(names, hidden) {
			t.Errorf("%s exposed: %v", hidden, names)
		}
	}
	if !slices.Contains(names, "dev_write_sim") {
		t.Errorf("write_sim missing: %v", names)
	}
}

func TestAutoAllowWorksWithoutApp(t *testing.T) {
	h := newHarness(t, time.Minute)
	a := h.agent("s1", 100)
	res := a.call("dev_echo", map[string]any{"text": "hi"})
	if res.IsError || text(res) != "hi" {
		t.Fatalf("echo: %+v", res)
	}
	c := h.lastCall()
	if c.Decision != model.DecisionAutoAllowed || c.SessionID != "claude:s1" || c.Plugin != "dev" || c.Tool != "echo" || c.ResultText != "hi" {
		t.Fatalf("log: %+v", c)
	}
}

func TestAppNotRunningAutoDenies(t *testing.T) {
	h := newHarness(t, time.Minute)
	a := h.agent("s1", 100)
	res := a.call("dev_write_sim", map[string]any{"text": "x"})
	if !res.IsError || !strings.Contains(text(res), "실행 중이 아니라") {
		t.Fatalf("want auto-deny, got %+v", res)
	}
	if c := h.lastCall(); c.Decision != model.DecisionAutoDenied || c.Reason != model.ReasonAppNotRunning {
		t.Fatalf("log: %+v", c)
	}
}

func TestApproveAndSessionGrant(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	a := h.agent("s1", 100)

	ch := a.callAsync("dev_write_sim", map[string]any{"text": "one"})
	ap := u.approval()
	if ap.Kind != model.KindPlugin || ap.Tool != "write_sim" || ap.SessionLabel != "proj-s1" || !ap.AllowSession ||
		string(ap.Input) != `{"text":"one"}` || !slices.Equal(ap.Reasons, []string{model.ReasonPolicy}) {
		t.Fatalf("approval: %+v", ap)
	}
	if s, _ := h.core.sessions.Get("claude:s1"); s.Status != model.StatusWaitingApproval {
		t.Fatalf("session should wait for approval: %s", s.Status)
	}
	u.answer(ap.ID, model.AnswerAllowSession)
	if res := <-ch; res.IsError || text(res) != "wrote: one" {
		t.Fatalf("result: %+v", res)
	}
	if c := h.lastCall(); c.Decision != model.DecisionUserAllowed || c.Reason != model.AnswerAllowSession {
		t.Fatalf("log: %+v", c)
	}
	// Second call: no card.
	if res := a.call("dev_write_sim", map[string]any{"text": "two"}); res.IsError {
		t.Fatalf("granted call failed: %+v", res)
	}
	if c := h.lastCall(); c.Decision != model.DecisionSessionAllowed {
		t.Fatalf("log: %+v", c)
	}
	// Another session still asks.
	b := h.agent("s2", 200)
	ch = b.callAsync("dev_write_sim", map[string]any{"text": "three"})
	ap = u.approval()
	if ap.SessionID != "claude:s2" {
		t.Fatalf("approval: %+v", ap)
	}
	u.answer(ap.ID, model.AnswerDeny)
	if res := <-ch; !res.IsError || !strings.Contains(text(res), "거부") {
		t.Fatalf("denied result: %+v", res)
	}
}

func TestNoSessionToolAsksEveryTime(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	a := h.agent("s1", 100)
	for i := 0; i < 2; i++ {
		ch := a.callAsync("dev_destroy_sim", map[string]any{"text": "x"})
		ap := u.approval()
		if ap.AllowSession {
			t.Fatalf("no_session tool offered a session grant: %+v", ap)
		}
		u.answer(ap.ID, model.AnswerAllow)
		<-ch
	}
}

func TestTaintVoidsGrants(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	a := h.agent("s1", 100)
	// Grant write_sim for the session.
	ch := a.callAsync("dev_write_sim", map[string]any{"text": "a"})
	u.answer(u.approval().ID, model.AnswerAllowSession)
	<-ch
	// Grant a shell command too (agent tool).
	bash := map[string]any{"command": "npm install"}
	go h.hook("s1", "PermissionRequest", "Bash", bash, 100)
	u.answer(u.approval().ID, model.AnswerAllowSession)
	waitFor(t, func() bool { c := h.lastCall(); return c.Kind == model.KindAgent })

	// Read untrusted content (scenario C).
	a.call("dev_taint_sim", map[string]any{"text": "x"})
	if s, _ := h.core.sessions.Get("claude:s1"); !s.Tainted {
		t.Fatal("session not tainted")
	}
	ch = a.callAsync("dev_write_sim", map[string]any{"text": "b"})
	ap := u.approval()
	if ap.AllowSession || !slices.Contains(ap.Reasons, model.ReasonTainted) {
		t.Fatalf("tainted approval: %+v", ap)
	}
	u.answer(ap.ID, model.AnswerAllow)
	<-ch
	// Q39: the shell grant is void as well.
	done := make(chan ipc.Message, 1)
	go func() { done <- h.hook("s1", "PermissionRequest", "Bash", bash, 100) }()
	ap = u.approval()
	if ap.Kind != model.KindAgent || !slices.Contains(ap.Reasons, model.ReasonTainted) {
		t.Fatalf("agent approval: %+v", ap)
	}
	u.answer(ap.ID, model.AnswerAllow)
	d, _ := ipc.Decode[ipc.HookDecision](<-done)
	if d.Behavior != ipc.BehaviorAllow {
		t.Fatalf("decision: %+v", d)
	}
}

func TestUnknownSessionIsStrict(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	a := h.agent("s1", 100)
	ch := make(chan *mcp.CallToolResult, 1)
	go func() { ch <- a.callNoHook("dev_write_sim", map[string]any{"text": "x"}) }()
	ap := u.approval()
	if ap.SessionID != "" || ap.AllowSession || !slices.Contains(ap.Reasons, model.ReasonUnknownSession) || ap.SessionLabel != "세션 불명" {
		t.Fatalf("approval: %+v", ap)
	}
	u.answer(ap.ID, model.AnswerAllow)
	<-ch
	if c := h.lastCall(); c.SessionID != "" {
		t.Fatalf("log should have no session: %+v", c)
	}
}

func TestUIDisconnectWhileWaiting(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	a := h.agent("s1", 100)
	ch := a.callAsync("dev_write_sim", map[string]any{"text": "x"})
	u.approval()
	u.close()
	res := <-ch
	if !res.IsError || !strings.Contains(text(res), "실행 중이 아니라") {
		t.Fatalf("got %+v", res)
	}
}

func TestApprovalTimeout(t *testing.T) {
	h := newHarness(t, 100*time.Millisecond)
	u := h.ui()
	a := h.agent("s1", 100)
	ch := a.callAsync("dev_write_sim", map[string]any{"text": "x"})
	ap := u.approval()
	c, _ := ipc.Decode[ipc.ApprovalCancelled](u.next(ipc.TypeApprovalCancelled))
	if c.ApprovalID != ap.ID || c.Reason != "timeout" {
		t.Fatalf("cancelled: %+v", c)
	}
	if res := <-ch; !res.IsError || !strings.Contains(text(res), "시간 초과") {
		t.Fatalf("got %+v", res)
	}
	if lc := h.lastCall(); lc.Decision != model.DecisionTimeout {
		t.Fatalf("log: %+v", lc)
	}
}

func TestUpstreamFailureIsToolError(t *testing.T) {
	h := newHarness(t, time.Minute)
	a := h.agent("s1", 100)
	res := a.call("dev_fail_sim", map[string]any{"text": "x"})
	if !res.IsError || !strings.Contains(text(res), "업스트림") {
		t.Fatalf("got %+v", res)
	}
	if c := h.lastCall(); c.Error == "" {
		t.Fatalf("log: %+v", c)
	}
}

func TestPermissionRequestFlow(t *testing.T) {
	h := newHarness(t, time.Minute)
	bash := map[string]any{"command": "npm install"}
	// No app: no decision, the agent shows its own prompt.
	d, _ := ipc.Decode[ipc.HookDecision](h.hook("s1", "PermissionRequest", "Bash", bash, 100))
	if d.Behavior != ipc.BehaviorNone {
		t.Fatalf("without app: %+v", d)
	}
	if c := h.lastCall(); c.Decision != model.DecisionPassthrough {
		t.Fatalf("log: %+v", c)
	}
	u := h.ui()
	done := make(chan ipc.Message, 1)
	go func() { done <- h.hook("s1", "PermissionRequest", "Bash", bash, 100) }()
	ap := u.approval()
	if ap.Kind != model.KindAgent || ap.Tool != "Bash" || !ap.AllowSession {
		t.Fatalf("approval: %+v", ap)
	}
	u.answer(ap.ID, model.AnswerDeny)
	d, _ = ipc.Decode[ipc.HookDecision](<-done)
	if d.Behavior != ipc.BehaviorDeny || d.Reason == "" {
		t.Fatalf("decision: %+v", d)
	}
	// Gateway tools never become agent approval cards.
	d, _ = ipc.Decode[ipc.HookDecision](h.hook("s1", "PermissionRequest", GatewayPrefix+"dev_write_sim", map[string]any{}, 100))
	if d.Behavior != "" {
		t.Fatalf("gateway tool PermissionRequest should just be acked: %+v", d)
	}
}

func TestHookDisconnectCancelsApproval(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	conn, err := ipc.Dial(h.sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "PermissionRequest", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	conn.Send(ipc.TypeHookEvent, "h", ipc.HookEvent{Agent: "claude", Input: raw, PID: 1})
	ap := u.approval()
	conn.Close() // the user answered in the terminal; the hook process exits
	c, _ := ipc.Decode[ipc.ApprovalCancelled](u.next(ipc.TypeApprovalCancelled))
	if c.ApprovalID != ap.ID || c.Reason != "cancelled" {
		t.Fatalf("cancelled: %+v", c)
	}
}

func TestSessionDeleteAndSnapshot(t *testing.T) {
	h := newHarness(t, time.Minute)
	a := h.agent("s1", 100)
	a.call("dev_echo", map[string]any{"text": "x"})
	u := h.ui()
	if len(u.snap.Sessions) != 1 || u.snap.Sessions[0].ID != "claude:s1" || !u.snap.Gateway.Running {
		t.Fatalf("snapshot: %+v", u.snap)
	}
	u.conn.Send(ipc.TypeSessionDelete, "del", ipc.SessionRef{SessionID: "claude:s1"})
	u.next(ipc.TypeSessionRemoved)
	calls, _ := h.st.QueryCalls(context.Background(), store.CallFilter{})
	if len(calls) != 0 {
		t.Fatalf("calls left: %d", len(calls))
	}
}

func TestPolicyOverrideChangesToolList(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	u.conn.Send(ipc.TypePolicySet, "p", ipc.PolicySet{Plugin: "dev", Tool: "echo", Level: "block"})
	u.next(ipc.TypePolicyState)
	if slices.Contains(h.core.gateway.ToolNames(), "dev_echo") {
		t.Fatal("blocked override still exposed")
	}
	over, _ := h.st.PolicyOverrides(context.Background())
	if over["dev_echo"] != "block" {
		t.Fatalf("override not persisted: %v", over)
	}
	u.conn.Send(ipc.TypePolicySet, "p2", ipc.PolicySet{Plugin: "dev", Tool: "destroy_sim", Level: "auto"})
	if m := u.next(ipc.TypeError); !strings.Contains(string(m.Data), "every time") {
		t.Fatalf("expected refusal, got %s", m.Data)
	}
}

// syncBuffer is a log destination safe for concurrent writers.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// An app update ships a new classification table; overrides set in the
// settings screen stay in the DB and apply to it (6-5), except that an auto
// override never lifts a tool the new table made no_session (Q33).
func TestPolicyOverridesSurviveTableUpdate(t *testing.T) {
	ctx := context.Background()
	st := memStore(t)
	h := startHarness(t, st, devTable(), time.Minute, nil)
	u := h.ui()
	for _, p := range []ipc.PolicySet{
		{Plugin: "dev", Tool: "write_sim", Level: policy.LevelAuto},
		{Plugin: "dev", Tool: "echo", Level: policy.LevelBlock},
		{Plugin: "dev", Tool: "fail_sim", Level: policy.LevelAsk},
	} {
		u.conn.Send(ipc.TypePolicySet, "p-"+p.Tool, p)
		if m := u.reply("p-" + p.Tool); m.Type != ipc.TypePolicyState {
			t.Fatalf("policy.set %s: %s %s", p.Tool, m.Type, m.Data)
		}
	}
	u.close()
	h.core.Shutdown(ctx)

	// farerod restarts with the new table.
	rules := maps.Clone(devplugin.Rules)
	rules["write_sim"] = policy.Rule{Level: policy.LevelAsk, NoSession: true}
	rules["echo"] = policy.Rule{Level: policy.LevelAsk}
	rules["fail_sim"] = policy.Rule{Level: policy.LevelAsk}
	tab := policy.DefaultTable()
	tab.AddPlugin(devplugin.Name, rules)
	var logs syncBuffer
	h = startHarness(t, st, tab, time.Minute, slog.New(slog.NewTextHandler(&logs, nil)))
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "dev_write_sim=auto") {
		t.Errorf("no startup warning for the ignored override:\n%s", out)
	}
	u = h.ui()
	u.conn.Send(ipc.TypePolicyGet, "g", nil)
	tools, err := ipc.Decode[[]policy.ToolInfo](u.reply("g"))
	if err != nil {
		t.Fatal(err)
	}
	byTool := map[string]policy.ToolInfo{}
	for _, ti := range tools {
		if ti.Plugin == devplugin.Name {
			byTool[ti.Tool] = ti
		}
	}
	for tool, want := range map[string]struct {
		level      string
		overridden bool
	}{
		"echo":      {policy.LevelBlock, true}, // default changed, the user's level stays
		"fail_sim":  {policy.LevelAsk, true},   // new default equals the user's level
		"write_sim": {policy.LevelAsk, false},  // no_session now: auto ignored
		"taint_sim": {policy.LevelAuto, false},
	} {
		if got := byTool[tool]; got.Level != want.level || got.Overridden != want.overridden {
			t.Errorf("%s: %+v, want level %s overridden %v", tool, got, want.level, want.overridden)
		}
	}
	names := h.core.gateway.ToolNames()
	if slices.Contains(names, "dev_echo") || !slices.Contains(names, "dev_write_sim") || !slices.Contains(names, "dev_fail_sim") {
		t.Errorf("tool list: %v", names)
	}
	// write_sim asks every time instead of running.
	a := h.agent("s1", 100)
	ch := a.callAsync("dev_write_sim", map[string]any{"text": "x"})
	ap := u.approval()
	if ap.Tool != "write_sim" || ap.AllowSession {
		t.Fatalf("approval: %+v", ap)
	}
	u.answer(ap.ID, model.AnswerDeny)
	<-ch
	// The ignored row is kept for a later table without no_session.
	if over, _ := st.PolicyOverrides(ctx); over["dev_write_sim"] != policy.LevelAuto || over["dev_echo"] != policy.LevelBlock {
		t.Fatalf("stored overrides: %v", over)
	}
}

type failingConnector struct{}

func (failingConnector) Connect(context.Context, map[string]string, func(ipc.PluginPrompt)) error {
	return errors.New("device flow expired")
}
func (failingConnector) Disconnect(context.Context) error { return nil }

type optionConnector struct{ reconnects int }

func (o *optionConnector) Connect(context.Context, map[string]string, func(ipc.PluginPrompt)) error {
	return nil
}
func (o *optionConnector) Disconnect(context.Context) error { return nil }
func (o *optionConnector) Reconnect(context.Context) error {
	o.reconnects++
	return errors.New("not connected")
}

func TestPluginConnectFailureIsReported(t *testing.T) {
	h := newHarness(t, time.Minute)
	h.core.connectors = map[string]Connector{"github": failingConnector{}}
	u := h.ui()
	u.conn.Send(ipc.TypePluginConnect, "c", ipc.PluginConnect{Plugin: "github"})
	p, _ := ipc.Decode[model.PluginState](u.next(ipc.TypePluginUpdated))
	if p.Plugin != "github" || p.Status != model.PluginError || !strings.Contains(p.Error, "device flow expired") {
		t.Fatalf("plugin state: %+v", p)
	}
}

func TestSetOptionOnDisconnectedPlugin(t *testing.T) {
	h := newHarness(t, time.Minute)
	oc := &optionConnector{}
	h.core.connectors = map[string]Connector{"github": oc}
	u := h.ui()
	u.conn.Send(ipc.TypePluginSetOption, "o", ipc.PluginSetOption{Plugin: "github", Key: "read_only", Value: "true"})
	p, _ := ipc.Decode[model.PluginState](u.next(ipc.TypePluginUpdated))
	if p.Options["read_only"] != "true" || oc.reconnects != 0 {
		t.Fatalf("state %+v, reconnects %d", p, oc.reconnects)
	}
}

func TestCallLoggedBroadcastIsTrimmed(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	big := strings.Repeat("x", 20000)
	go h.hook("s1", "PermissionRequest", "Write", map[string]any{"file_path": "/tmp/a", "content": big}, 1)
	u.answer(u.approval().ID, model.AnswerDeny)
	c, _ := ipc.Decode[model.Call](u.next(ipc.TypeCallLogged))
	// The bounds in these tests are literals, so raising a limit constant
	// fails them.
	if len(c.Input) > 8<<10 || !strings.Contains(string(c.Input), `"file_path":"/tmp/a"`) {
		t.Fatalf("broadcast input not trimmed to a bounded JSON object: %d bytes", len(c.Input))
	}
	// The row keeps as much as the Q46 budget allows (16 KB by default).
	stored := h.lastCall()
	if len(stored.Input) > store.DefaultResultLimit || len(stored.Input) < store.DefaultResultLimit-100 {
		t.Fatalf("stored input %d bytes, want close to %d", len(stored.Input), store.DefaultResultLimit)
	}
	inputObject(t, stored.Input, store.DefaultResultLimit, "file_path", "/tmp/a")
}

// A log.result_limit outside 1 KB–1 MB, or not a number, falls back to the
// default (Q46) rather than being used.
func TestResultLimitIgnoresBadSetting(t *testing.T) {
	h := newHarness(t, time.Minute)
	ctx := context.Background()
	for _, v := range []string{"99999999", "512", "x", ""} {
		if err := h.st.SetSetting(ctx, settingResultLimit, v); err != nil {
			t.Fatal(err)
		}
		if got := h.core.resultLimit(ctx); got != store.DefaultResultLimit {
			t.Fatalf("setting %q: limit %d", v, got)
		}
	}
	h.st.SetSetting(ctx, settingResultLimit, "4096")
	if got := h.core.resultLimit(ctx); got != 4096 {
		t.Fatalf("valid setting: limit %d", got)
	}
}

// inputObject decodes a bounded input and checks it is still a JSON object
// no larger than limit that kept its short field.
func inputObject(t *testing.T, raw json.RawMessage, limit int, key, want string) map[string]any {
	t.Helper()
	if len(raw) > limit {
		t.Fatalf("input %d bytes, limit %d", len(raw), limit)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m[key] != want {
		t.Fatalf("input not a JSON object with %s=%q: %v %.80s", key, want, err, raw)
	}
	return m
}

// A Write of a file over 1 MB: the card and the log row stay bounded, while
// the whole content is what policy and the agent see.
func TestApprovalCardBoundsAgentInput(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	big := strings.Repeat("나", 400_000) // 1.2 MB, over the 1 MiB card bound
	go h.hook("s1", "PermissionRequest", "Write", map[string]any{"file_path": "/tmp/big", "content": big}, 1)
	ap := u.approval()
	m := inputObject(t, ap.Input, 1<<20, "file_path", "/tmp/big")
	if c, _ := m["content"].(string); !strings.HasPrefix(c, "나나") || !strings.Contains(c, "bytes]") {
		t.Fatalf("content not cut with a marker: %.40q", c)
	}
	// A UI that connects while the card is open gets the bounded copy too.
	u2 := h.ui()
	if len(u2.snap.Approvals) != 1 {
		t.Fatalf("snapshot: %+v", u2.snap.Approvals)
	}
	inputObject(t, u2.snap.Approvals[0].Input, 1<<20, "file_path", "/tmp/big")
	u.answer(ap.ID, model.AnswerDeny)
	stored := h.lastCall()
	inputObject(t, stored.Input, store.DefaultResultLimit, "file_path", "/tmp/big")
	c, _ := ipc.Decode[model.Call](u.next(ipc.TypeCallLogged))
	inputObject(t, c.Input, 8<<10, "file_path", "/tmp/big")
}

// A plugin call with an argument over 1 MB (a Resend attachment) reaches the
// upstream unchanged; only the card and the row are bounded.
func TestUpstreamGetsFullPluginInput(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	a := h.agent("s1", 100)
	big := strings.Repeat("Zm9v", 300_000) // 1.2 MB, over the 1 MiB card bound
	ch := a.callAsync("dev_write_sim", map[string]any{"text": big, "to": "a@b.c"})
	ap := u.approval()
	inputObject(t, ap.Input, 1<<20, "to", "a@b.c")
	// Correlation matched the PreToolUse hook on the full arguments.
	if ap.SessionID != "claude:s1" || !ap.AllowSession {
		t.Fatalf("card not tied to the session: %q allow_session=%v", ap.SessionID, ap.AllowSession)
	}
	u.answer(ap.ID, model.AnswerAllow)
	if res := <-ch; res.IsError || text(res) != "wrote: "+big {
		t.Fatalf("upstream did not get the full input: err=%v len=%d", res.IsError, len(text(res)))
	}
	stored := h.lastCall()
	inputObject(t, stored.Input, store.DefaultResultLimit, "to", "a@b.c")
	if stored.Decision != model.DecisionUserAllowed || stored.SessionID != "claude:s1" {
		t.Fatalf("log: %+v", stored)
	}
	// The 1.2 MB result: the row keeps the Q46 budget, call.logged 2 KB, and
	// both carry the full size.
	resultBytes := len("wrote: ") + len(big)
	if len(stored.ResultText) > store.DefaultResultLimit || stored.ResultBytes != resultBytes {
		t.Fatalf("stored result %d bytes of %d", len(stored.ResultText), stored.ResultBytes)
	}
	c, _ := ipc.Decode[model.Call](u.next(ipc.TypeCallLogged))
	if len(c.ResultText) > 2<<10 || c.ResultBytes != resultBytes {
		t.Fatalf("broadcast result %d bytes of %d", len(c.ResultText), c.ResultBytes)
	}
	inputObject(t, c.Input, 8<<10, "to", "a@b.c")
}

// A session grant for a Bash command is scoped by the whole command (Q38),
// not by the bounded copy on the card: the same 1.2 MB command runs without a
// card, and one that differs only past what the card shows still asks.
func TestSessionGrantUsesFullAgentInput(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	cmdA := strings.Repeat("a", 1_200_000) // over the 1 MiB card bound
	cmdB := cmdA[:len(cmdA)-1] + "b"
	bash := func(cmd string) chan ipc.HookDecision {
		done := make(chan ipc.HookDecision, 1)
		go func() {
			d, _ := ipc.Decode[ipc.HookDecision](h.hook("s1", "PermissionRequest", "Bash", map[string]any{"command": cmd}, 1))
			done <- d
		}()
		return done
	}

	done := bash(cmdA)
	u.answer(u.approval().ID, model.AnswerAllowSession)
	if d := <-done; d.Behavior != ipc.BehaviorAllow {
		t.Fatalf("first: %+v", d)
	}

	done = bash(cmdA)
	select {
	case d := <-done:
		if d.Behavior != ipc.BehaviorAllow {
			t.Fatalf("granted command: %+v", d)
		}
	case <-time.After(3 * time.Second):
		u.answer(u.approval().ID, model.AnswerDeny)
		<-done
		t.Fatal("the granted command asked again")
	}

	done = bash(cmdB)
	u.answer(u.approval().ID, model.AnswerDeny)
	if d := <-done; d.Behavior != ipc.BehaviorDeny {
		t.Fatalf("other command: %+v", d)
	}
}

// blockingSecrets holds every read until release is closed, like a Keychain
// prompt the user has not answered yet.
type blockingSecrets struct {
	secret.Store
	release chan struct{}
}

func (b blockingSecrets) Get(key string) (string, error) {
	<-b.release
	return b.Store.Get(key)
}

// With ad-hoc signing every update makes the Keychain ask again, and the
// user may answer late (M0 2026-10-06). The app must connect meanwhile, and
// learn about the gateway once it starts.
func TestUIConnectsBeforeKeychainAnswers(t *testing.T) {
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	secrets := blockingSecrets{Store: secret.NewMemoryStore(), release: make(chan struct{})}

	created := make(chan *Core, 1)
	go func() {
		c, err := New(ctx, Options{Version: "test", Store: st, Secrets: secrets, Table: policy.DefaultTable()})
		if err != nil {
			t.Error(err)
		}
		created <- c
	}()
	var c *Core
	select {
	case c = <-created:
	case <-time.After(2 * time.Second):
		close(secrets.release)
		t.Fatal("New waited for the Keychain")
	}

	dir, _ := os.MkdirTemp("/tmp", "frc")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := ipc.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	go ipc.Serve(ctx, l, c.Serve)
	h := &harness{t: t, core: c, sock: sock, st: st}
	u := h.ui()
	if u.snap.Gateway.Running {
		t.Fatalf("gateway running before its secret was read: %+v", u.snap.Gateway)
	}

	started := make(chan error, 1)
	go func() { started <- c.StartGateway(ctx) }()
	close(secrets.release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Shutdown(context.Background()) })
	c.BroadcastSnapshot(ctx)
	snap, _ := ipc.Decode[ipc.StateSnapshot](u.next(ipc.TypeStateSnapshot))
	if !snap.Gateway.Running || snap.Gateway.URL == "" {
		t.Fatalf("snapshot after start: %+v", snap.Gateway)
	}
}

// farerod pushes the agent config status when it fixed paths on its own
// (Q63), so the app can tell the user without opening settings.
func TestNotifyReachesApps(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	h.core.Notify(ipc.TypeAgentCfgStatus, ipc.AgentCfgStatus{Agent: "claude", Message: "고쳤습니다"})
	st, err := ipc.Decode[ipc.AgentCfgStatus](u.next(ipc.TypeAgentCfgStatus))
	if err != nil || st.Message != "고쳤습니다" {
		t.Fatalf("%+v %v", st, err)
	}
}

// partialPlugin is the dev plugin with fail_sim missing from its tool list,
// like an upstream that renamed or dropped a classified tool.
type partialPlugin struct{ upstream.Plugin }

func (p partialPlugin) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	all, err := p.Plugin.ListTools(ctx)
	return slices.DeleteFunc(all, func(t *mcp.Tool) bool { return t.Name == "fail_sim" }), err
}

func TestPluginToolsReportsClassification(t *testing.T) {
	h := newHarness(t, time.Minute)
	h.core.Plugins().Set(partialPlugin{devplugin.New()})
	u := h.ui()
	u.conn.Send(ipc.TypePluginTools, "t", ipc.PluginRef{Plugin: "dev"})
	got, _ := ipc.Decode[ipc.PluginTools](u.next(ipc.TypePluginTools))
	byName := map[string]ipc.PluginTool{}
	for _, x := range got.Tools {
		byName[x.Name] = x
	}
	if x := byName["unclassified_sim"]; x.Name == "" || x.Classified || x.Exposed {
		t.Errorf("unclassified_sim: %+v", x)
	}
	if x := byName["blocked_sim"]; !x.Classified || x.Exposed || x.Level != policy.LevelBlock {
		t.Errorf("blocked_sim: %+v", x)
	}
	if x := byName["write_sim"]; !x.Classified || !x.Exposed || x.Level != policy.LevelAsk {
		t.Errorf("write_sim: %+v", x)
	}
	if !slices.Equal(got.Missing, []string{"fail_sim"}) {
		t.Errorf("missing: %v", got.Missing)
	}
	if !slices.IsSortedFunc(got.Tools, func(a, b ipc.PluginTool) int { return strings.Compare(a.Name, b.Name) }) {
		t.Errorf("tools not sorted by name: %+v", got.Tools)
	}

	u.conn.Send(ipc.TypePluginTools, "g", ipc.PluginRef{Plugin: "github"})
	if m := u.next(ipc.TypeError); !strings.Contains(string(m.Data), "not connected") {
		t.Fatalf("expected not connected, got %s", m.Data)
	}
}

// A hook event without a session id is acknowledged and ignored.
func TestHookWithoutSessionIDIsIgnored(t *testing.T) {
	h := newHarness(t, time.Minute)
	m := h.hookRaw(map[string]any{"hook_event_name": "SessionStart", "cwd": "/tmp/p"}, 100)
	if m.Type != ipc.TypeHookAck {
		t.Fatalf("reply: %+v", m)
	}
	if l := h.core.sessions.List(); len(l) != 0 {
		t.Fatalf("sessions: %+v", l)
	}
}

// An event from a hook that did not name its agent counts as Claude Code's.
func TestHookAgentDefaultsToClaude(t *testing.T) {
	h := newHarness(t, time.Minute)
	conn, err := ipc.Dial(h.sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	raw, _ := json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "SessionStart", "cwd": "/tmp/p"})
	conn.Send(ipc.TypeHookEvent, "h", ipc.HookEvent{Input: raw, PID: 100})
	if _, err := conn.Read(); err != nil {
		t.Fatal(err)
	}
	if s, ok := h.core.sessions.Get("claude:s1"); !ok || s.Agent != model.AgentClaude {
		t.Fatalf("session: %+v %v", s, ok)
	}
}

// The gateway port picked on the first run is saved and used again after a
// restart (Claude Code's registration names it).
func TestGatewayPortSurvivesRestart(t *testing.T) {
	h := newHarness(t, time.Minute)
	ctx := context.Background()
	port := h.core.GatewayInfo().Port
	if saved, _ := h.st.Setting(ctx, settingGatewayPort); port == 0 || saved != strconv.Itoa(port) {
		t.Fatalf("port %d, saved %q", port, saved)
	}
	h.core.Shutdown(ctx)
	h2 := startHarness(t, h.st, devTable(), time.Minute, nil)
	if got := h2.core.GatewayInfo(); !got.Running || got.Port != port {
		t.Fatalf("after restart: %+v, want port %d", got, port)
	}
}

// Exposed tools are sorted by name and keep the upstream's input schema.
func TestExposedToolsSortedWithSchema(t *testing.T) {
	h := newHarness(t, time.Minute)
	h.core.mu.Lock()
	tools := slices.Clone(h.core.tools)
	h.core.mu.Unlock()
	if len(tools) < 2 || !slices.IsSortedFunc(tools, func(a, b *mcp.Tool) int { return strings.Compare(a.Name, b.Name) }) {
		t.Fatalf("tools not sorted: %d", len(tools))
	}
	for _, x := range tools {
		if x.Name == "dev_write_sim" {
			if b, _ := json.Marshal(x.InputSchema); !strings.Contains(string(b), `"text"`) {
				t.Fatalf("schema replaced: %s", b)
			}
			return
		}
	}
	t.Fatal("dev_write_sim not exposed")
}

// Upstream tools missing from the table are logged once per change, not on
// every refresh.
func TestHiddenToolsLoggedOnChange(t *testing.T) {
	var logs syncBuffer
	h := startHarness(t, memStore(t), devTable(), time.Minute, slog.New(slog.NewTextHandler(&logs, nil)))
	// Startup refreshed once; two more refreshes with the same tools must
	// not log again.
	h.core.RefreshTools(context.Background())
	h.core.RefreshTools(context.Background())
	const msg = "not in the classification table are hidden"
	if n := strings.Count(logs.String(), msg); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "unclassified_sim") {
		t.Fatalf("hidden tool not named:\n%s", logs.String())
	}
}

// The app gets an error for an answer to a card that is not open, and OK
// for a real one.
func TestApprovalResponseReplies(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	u.conn.Send(ipc.TypeApprovalResponse, "x", ipc.ApprovalResponse{ApprovalID: "nope", Answer: model.AnswerAllow})
	if m := u.reply("x"); m.Type != ipc.TypeError {
		t.Fatalf("unknown card: %+v", m)
	}
	go h.hook("s1", "PermissionRequest", "Bash", map[string]any{"command": "ls"}, 100)
	ap := u.approval()
	u.answer(ap.ID, model.AnswerDeny)
	if m := u.reply("r-" + ap.ID); m.Type != ipc.TypeOK {
		t.Fatalf("real card: %+v", m)
	}
}
