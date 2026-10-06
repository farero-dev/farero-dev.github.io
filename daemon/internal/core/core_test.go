package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/gateway"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/policy"
	"github.com/farero-dev/farero/daemon/internal/secret"
	"github.com/farero-dev/farero/daemon/internal/store"
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
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tab := policy.DefaultTable()
	tab.AddPlugin(devplugin.Name, devplugin.Rules)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c, err := New(ctx, Options{Version: "test", Store: st, Secrets: secret.NewMemoryStore(), Table: tab, ApprovalTimeout: approvalTimeout})
	if err != nil {
		t.Fatal(err)
	}
	c.Plugins().Set(devplugin.New())
	c.RefreshTools(ctx)
	if err := c.StartGateway(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Shutdown(context.Background()) })

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

func (u *fakeUI) next(typ string) ipc.Message {
	u.t.Helper()
	timeout := time.After(5 * time.Second)
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
	h.t.Cleanup(func() { cs.Close() })
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
	if len(c.Input) > 200 || !strings.Contains(string(c.Input), "bytes") {
		t.Fatalf("broadcast input not trimmed: %d bytes", len(c.Input))
	}
	stored := h.lastCall()
	if len(stored.Input) < 20000 {
		t.Fatalf("stored input was trimmed: %d", len(stored.Input))
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
