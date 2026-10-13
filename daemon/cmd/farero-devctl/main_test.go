package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
)

// fakeDaemon listens on a short socket under /tmp (FARERO_SOCKET). For each
// UI connection it answers ui.hello with a state.snapshot, then runs script.
// Every message the client sends (hello included) is sent to the returned
// channel.
func fakeDaemon(t *testing.T, script func(c *ipc.Conn)) chan ipc.Message {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "frd") // socket paths are limited to 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := ipc.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	got := make(chan ipc.Message, 16)
	go ipc.Serve(ctx, l, func(_ context.Context, c *ipc.Conn, first ipc.Message) {
		got <- first
		c.Send(ipc.TypeStateSnapshot, first.ID, map[string]any{"sessions": []any{}})
		if script != nil {
			go script(c)
		}
		// Forward whatever the client sends next until it hangs up (the
		// handler must not return earlier: that would close the conn).
		for {
			m, err := c.Read()
			if err != nil {
				return
			}
			got <- m
		}
	})
	t.Setenv("FARERO_SOCKET", sock)
	return got
}

// reply sends a message to the client. It may be sent before the client's
// request arrives: the client only reads replies after sending.
func reply(c *ipc.Conn, typ, id string, data any) { c.Send(typ, id, data) }

// next returns the next message the daemon received.
func next(t *testing.T, got chan ipc.Message) ipc.Message {
	t.Helper()
	select {
	case m := <-got:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("daemon received nothing")
		return ipc.Message{}
	}
}

// request returns the message after the hello and checks the hello.
func request(t *testing.T, got chan ipc.Message) ipc.Message {
	t.Helper()
	h := next(t, got)
	if h.Type != ipc.TypeUIHello || h.ID != "hello" {
		t.Fatalf("first message = %+v", h)
	}
	var hello ipc.UIHello
	json.Unmarshal(h.Data, &hello)
	if hello.Version != "devctl" {
		t.Fatalf("hello version = %q", hello.Version)
	}
	return next(t, got)
}

func noMore(t *testing.T, got chan ipc.Message) {
	t.Helper()
	select {
	case m := <-got:
		t.Fatalf("unexpected message %+v", m)
	case <-time.After(150 * time.Millisecond):
	}
}

func data[T any](t *testing.T, m ipc.Message) T {
	t.Helper()
	v, err := ipc.Decode[T](m)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func runCtl(args ...string) (code int, out, errs string) {
	var o, e bytes.Buffer
	code = run(args, strings.NewReader(""), &o, &e)
	return code, o.String(), e.String()
}

func TestNoArgs(t *testing.T) {
	code, out, errs := runCtl()
	if code != 2 || out != "" || errs != "usage: farero-devctl watch|answer|log|policy|agentcfg|plugin|settings|session\n" {
		t.Fatalf("%d %q %q", code, out, errs)
	}
}

func TestUnreachable(t *testing.T) {
	t.Setenv("FARERO_SOCKET", "/tmp/frd-no-such.sock")
	code, out, errs := runCtl("log")
	if code != 1 || out != "" || !strings.HasPrefix(errs, "farerod not reachable: ") {
		t.Fatalf("%d %q %q", code, out, errs)
	}
}

func TestSnapshotReadFails(t *testing.T) {
	// A daemon that closes right after the hello gives no snapshot.
	dir, _ := os.MkdirTemp("/tmp", "frd")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "d.sock")
	l, err := ipc.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ipc.Serve(ctx, l, func(_ context.Context, c *ipc.Conn, _ ipc.Message) { c.Close() })
	t.Setenv("FARERO_SOCKET", sock)
	code, out, errs := runCtl("log")
	if code != 1 || out != "" || errs == "" {
		t.Fatalf("%d %q %q", code, out, errs)
	}
}

func TestUnknownCommand(t *testing.T) {
	got := fakeDaemon(t, nil)
	code, out, errs := runCtl("bogus")
	if code != 2 || out != "" || errs != "unknown command bogus\n" {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	if m := next(t, got); m.Type != ipc.TypeUIHello {
		t.Fatal(m)
	}
}

// printed checks a printReply output: the reply type, then indented JSON.
func printed(t *testing.T, out, typ string, want any) {
	t.Helper()
	head, body, ok := strings.Cut(out, "\n")
	if !ok || head != typ {
		t.Fatalf("output %q", out)
	}
	b, _ := json.MarshalIndent(want, "", "  ")
	if body != string(b)+"\n" {
		t.Fatalf("body %q, want %q", body, string(b))
	}
}

func TestAnswer(t *testing.T) {
	got := fakeDaemon(t, func(c *ipc.Conn) {
		reply(c, ipc.TypeOK, "other", nil) // different id is skipped
		reply(c, ipc.TypeOK, "r", map[string]any{"done": true})
	})
	code, out, errs := runCtl("answer", "ap1", "allow_session")
	if code != 0 || errs != "" {
		t.Fatalf("%d %q", code, errs)
	}
	printed(t, out, "ok", map[string]any{"done": true})
	m := request(t, got)
	if m.Type != ipc.TypeApprovalResponse || m.ID != "r" {
		t.Fatal(m)
	}
	if r := data[ipc.ApprovalResponse](t, m); r.ApprovalID != "ap1" || r.Answer != "allow_session" {
		t.Fatalf("%+v", r)
	}
}

func TestAnswerUsage(t *testing.T) {
	for _, args := range [][]string{{"answer"}, {"answer", "x"}, {"answer", "x", "allow", "y"}} {
		got := fakeDaemon(t, nil)
		code, out, errs := runCtl(args...)
		if code != 2 || out != "" || errs != "usage: farero-devctl answer <approval id> allow|allow_session|deny\n" {
			t.Fatalf("%v: %d %q %q", args, code, out, errs)
		}
		next(t, got) // hello
		noMore(t, got)
	}
}

func TestReplyConnectionLost(t *testing.T) {
	fakeDaemon(t, func(c *ipc.Conn) { c.Close() })
	code, out, errs := runCtl("answer", "a", "deny")
	if code != 1 || out != "" || errs == "" {
		t.Fatalf("%d %q %q", code, out, errs)
	}
}

func TestLog(t *testing.T) {
	got := fakeDaemon(t, func(c *ipc.Conn) { reply(c, ipc.TypeLogResult, "q", []string{"a"}) })
	code, out, _ := runCtl("log", "-q", "railway", "-n", "5")
	if code != 0 {
		t.Fatal(code)
	}
	printed(t, out, "log.result", []string{"a"})
	m := request(t, got)
	if m.Type != ipc.TypeLogQuery || m.ID != "q" {
		t.Fatal(m)
	}
	var f map[string]any
	json.Unmarshal(m.Data, &f)
	if f["Query"] != "railway" && f["query"] != "railway" {
		t.Fatalf("%s", m.Data)
	}
	if !strings.Contains(string(m.Data), "5") {
		t.Fatalf("%s", m.Data)
	}
}

func TestLogDefaults(t *testing.T) {
	got := fakeDaemon(t, func(c *ipc.Conn) { reply(c, ipc.TypeLogResult, "q", []string{}) })
	if code, _, _ := runCtl("log"); code != 0 {
		t.Fatal(code)
	}
	m := request(t, got)
	var f map[string]any
	json.Unmarshal(m.Data, &f)
	for k, v := range f {
		if strings.EqualFold(k, "limit") && v != float64(20) {
			t.Fatalf("limit = %v", v)
		}
		if strings.EqualFold(k, "query") && v != "" {
			t.Fatalf("query = %v", v)
		}
	}
	if !strings.Contains(string(m.Data), "20") {
		t.Fatalf("%s", m.Data)
	}
}

func TestFlagErrors(t *testing.T) {
	for _, args := range [][]string{{"log", "-n", "abc"}, {"log", "-x"}, {"watch", "-x"}} {
		got := fakeDaemon(t, nil)
		code, out, errs := runCtl(args...)
		if code != 2 || out != "" || errs == "" {
			t.Fatalf("%v: %d %q %q", args, code, out, errs)
		}
		next(t, got)
		noMore(t, got)
	}
}

func TestFlagHelp(t *testing.T) {
	fakeDaemon(t, nil)
	code, out, errs := runCtl("log", "-h")
	if code != 0 || out != "" || !strings.Contains(errs, "-q") {
		t.Fatalf("%d %q %q", code, out, errs)
	}
}

func TestPolicyGet(t *testing.T) {
	got := fakeDaemon(t, func(c *ipc.Conn) { reply(c, ipc.TypePolicyState, "p", map[string]int{"x": 1}) })
	code, out, _ := runCtl("policy")
	if code != 0 {
		t.Fatal(code)
	}
	printed(t, out, "policy.state", map[string]int{"x": 1})
	m := request(t, got)
	if m.Type != ipc.TypePolicyGet || m.ID != "p" {
		t.Fatal(m)
	}
}

func TestPolicySet(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		level string
	}{
		{[]string{"policy", "set", "railway", "deploy", "ask"}, "ask"},
		{[]string{"policy", "set", "railway", "deploy"}, ""},
	} {
		got := fakeDaemon(t, func(c *ipc.Conn) { reply(c, ipc.TypeOK, "p", nil) })
		code, out, errs := runCtl(tc.args...)
		if code != 0 || errs != "" || !strings.HasPrefix(out, "ok\n") {
			t.Fatalf("%d %q %q", code, out, errs)
		}
		m := request(t, got)
		if m.Type != ipc.TypePolicySet || m.ID != "p" {
			t.Fatal(m)
		}
		want := ipc.PolicySet{Plugin: "railway", Tool: "deploy", Level: tc.level}
		if r := data[ipc.PolicySet](t, m); r != want {
			t.Fatalf("%+v", r)
		}
	}
}

func TestPolicyUsage(t *testing.T) {
	for _, args := range [][]string{
		{"policy", "get"},
		{"policy", "set"},
		{"policy", "set", "a"},
		{"policy", "set", "a", "b", "ask", "extra"},
	} {
		got := fakeDaemon(t, nil)
		code, out, errs := runCtl(args...)
		if code != 2 || out != "" || errs != "usage: farero-devctl policy [set <plugin> <tool> [auto|ask|block]]\n" {
			t.Fatalf("%v: %d %q %q", args, code, out, errs)
		}
		next(t, got)
		noMore(t, got)
	}
}

func TestAgentcfg(t *testing.T) {
	for _, tc := range []struct {
		args []string
		typ  string
	}{
		{[]string{"agentcfg"}, "agentcfg.status"},
		{[]string{"agentcfg", "plan"}, "agentcfg.plan"},
		{[]string{"agentcfg", "apply"}, "agentcfg.apply"},
	} {
		got := fakeDaemon(t, func(c *ipc.Conn) { reply(c, ipc.TypeOK, "a", nil) })
		if code, out, _ := runCtl(tc.args...); code != 0 || !strings.HasPrefix(out, "ok\n") {
			t.Fatalf("%d %q", code, out)
		}
		m := request(t, got)
		if m.Type != tc.typ || m.ID != "a" {
			t.Fatal(m)
		}
		if r := data[ipc.AgentRef](t, m); r.Agent != "claude" {
			t.Fatalf("%+v", r)
		}
	}
}

func TestSession(t *testing.T) {
	got := fakeDaemon(t, func(c *ipc.Conn) { reply(c, ipc.TypeOK, "s", nil) })
	if code, out, _ := runCtl("session", "delete", "s1"); code != 0 || !strings.HasPrefix(out, "ok\n") {
		t.Fatalf("%d %q", code, out)
	}
	m := request(t, got)
	if m.Type != ipc.TypeSessionDelete || m.ID != "s" {
		t.Fatal(m)
	}
	if r := data[ipc.SessionRef](t, m); r.SessionID != "s1" {
		t.Fatalf("%+v", r)
	}
}

func TestSessionUsage(t *testing.T) {
	for _, args := range [][]string{{"session"}, {"session", "delete"}, {"session", "remove", "x"}, {"session", "delete", "x", "y"}} {
		got := fakeDaemon(t, nil)
		code, out, errs := runCtl(args...)
		if code != 2 || out != "" || errs != "usage: farero-devctl session delete <session id>\n" {
			t.Fatalf("%v: %d %q %q", args, code, out, errs)
		}
		next(t, got)
		noMore(t, got)
	}
}

func TestSettingsGet(t *testing.T) {
	got := fakeDaemon(t, func(c *ipc.Conn) { reply(c, ipc.TypeOK, "g", map[string]int{"a": 1}) })
	code, out, _ := runCtl("settings")
	if code != 0 {
		t.Fatal(code)
	}
	printed(t, out, "ok", map[string]int{"a": 1})
	if m := request(t, got); m.Type != ipc.TypeSettingsGet || m.ID != "g" {
		t.Fatal(m)
	}
}

func TestSettingsSet(t *testing.T) {
	ptrI := func(n int) *int { return &n }
	ptrB := func(b bool) *bool { return &b }
	for _, tc := range []struct {
		args []string
		want ipc.SettingsSet
	}{
		{[]string{"limit", "4096"}, ipc.SettingsSet{ResultLimitBytes: ptrI(4096)}},
		{[]string{"update", "on"}, ipc.SettingsSet{UpdateCheck: ptrB(true)}},
		{[]string{"update", "off"}, ipc.SettingsSet{UpdateCheck: ptrB(false)}},
	} {
		got := fakeDaemon(t, func(c *ipc.Conn) { reply(c, ipc.TypeOK, "s", nil) })
		code, out, errs := runCtl(append([]string{"settings"}, tc.args...)...)
		if code != 0 || errs != "" || !strings.HasPrefix(out, "ok\n") {
			t.Fatalf("%v: %d %q %q", tc.args, code, out, errs)
		}
		m := request(t, got)
		if m.Type != ipc.TypeSettingsSet || m.ID != "s" {
			t.Fatal(m)
		}
		if r := data[ipc.SettingsSet](t, m); !reflect.DeepEqual(r, tc.want) {
			t.Fatalf("%v: %s", tc.args, m.Data)
		}
	}
}

func TestSettingsUsage(t *testing.T) {
	for _, args := range [][]string{
		{"settings", "limit"},
		{"settings", "limit", "abc"},
		{"settings", "limit", "1", "2"},
		{"settings", "update", "maybe"},
		{"settings", "update"},
		{"settings", "color", "red"},
	} {
		got := fakeDaemon(t, nil)
		code, out, errs := runCtl(args...)
		if code != 2 || out != "" || errs != settingsUsage+"\n" {
			t.Fatalf("%v: %d %q %q", args, code, out, errs)
		}
		next(t, got)
		noMore(t, got)
	}
}

func TestPluginList(t *testing.T) {
	got := fakeDaemon(t, func(c *ipc.Conn) { reply(c, ipc.TypeOK, "l", []string{"gh"}) })
	code, out, _ := runCtl("plugin")
	if code != 0 {
		t.Fatal(code)
	}
	printed(t, out, "ok", []string{"gh"})
	if m := request(t, got); m.Type != ipc.TypePluginList || m.ID != "l" {
		t.Fatal(m)
	}
}

func TestPluginSimpleOps(t *testing.T) {
	for _, tc := range []struct {
		args []string
		typ  string
		id   string
		want any
	}{
		{[]string{"plugin", "disconnect", "gh"}, ipc.TypePluginDisconnect, "d", ipc.PluginRef{Plugin: "gh"}},
		{[]string{"plugin", "option", "gh", "k", "v"}, ipc.TypePluginSetOption, "o", ipc.PluginSetOption{Plugin: "gh", Key: "k", Value: "v"}},
		{[]string{"plugin", "tools", "gh"}, ipc.TypePluginTools, "t", ipc.PluginRef{Plugin: "gh"}},
	} {
		got := fakeDaemon(t, func(c *ipc.Conn) { reply(c, ipc.TypeOK, tc.id, nil) })
		code, out, errs := runCtl(tc.args...)
		if code != 0 || errs != "" || !strings.HasPrefix(out, "ok\n") {
			t.Fatalf("%v: %d %q %q", tc.args, code, out, errs)
		}
		m := request(t, got)
		if m.Type != tc.typ || m.ID != tc.id {
			t.Fatalf("%v: %+v", tc.args, m)
		}
		var gotV, wantV any
		json.Unmarshal(m.Data, &gotV)
		b, _ := json.Marshal(tc.want)
		json.Unmarshal(b, &wantV)
		if !reflect.DeepEqual(gotV, wantV) {
			t.Fatalf("%v: %s want %s", tc.args, m.Data, b)
		}
	}
}

func TestPluginUsage(t *testing.T) {
	for _, args := range [][]string{
		{"plugin", "connect"},
		{"plugin", "connect", "gh", "novalue"},
		{"plugin", "option", "gh", "k"},
		{"plugin", "option", "gh", "k", "v", "x"},
		{"plugin", "frobnicate", "gh"},
	} {
		got := fakeDaemon(t, nil)
		code, out, errs := runCtl(args...)
		if code != 2 || out != "" || errs != pluginUsage+"\n" {
			t.Fatalf("%v: %d %q %q", args, code, out, errs)
		}
		next(t, got)
		noMore(t, got)
	}
}

func stubOpen(t *testing.T) *[]string {
	t.Helper()
	var urls []string
	old := openURL
	openURL = func(u string) { urls = append(urls, u) }
	t.Cleanup(func() { openURL = old })
	return &urls
}

func state(plugin, status string) model.PluginState {
	return model.PluginState{Plugin: plugin, Status: status}
}

func TestPluginConnectFlow(t *testing.T) {
	urls := stubOpen(t)
	got := fakeDaemon(t, func(c *ipc.Conn) {
		reply(c, ipc.TypeError, "zzz", map[string]string{"message": "other request"}) // not ours
		reply(c, ipc.TypePluginPrompt, "", ipc.PluginPrompt{Plugin: "other", URL: "https://other"})
		reply(c, ipc.TypePluginPrompt, "", ipc.PluginPrompt{Plugin: "gh", UserCode: "AB-12", URL: "https://dev/code"})
		reply(c, ipc.TypePluginPrompt, "", ipc.PluginPrompt{Plugin: "gh", URL: "https://dev/auth"})
		reply(c, ipc.TypePluginUpdated, "", state("other", model.PluginConnected))
		reply(c, ipc.TypePluginUpdated, "", state("gh", "connecting"))
		reply(c, ipc.TypePluginUpdated, "", state("gh", model.PluginConnected))
	})
	code, out, errs := runCtl("plugin", "connect", "gh", "token=a=b", "region=eu")
	if code != 0 || errs != "" {
		t.Fatalf("%d %q", code, errs)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 5 ||
		lines[0] != "code: AB-12" || lines[1] != "open: https://dev/code" ||
		lines[2] != "open: https://dev/auth" ||
		!strings.Contains(lines[3], " plugin.updated ") || !strings.Contains(lines[3], `"connecting"`) ||
		!strings.Contains(lines[4], `"connected"`) || strings.Contains(out, "https://other") {
		t.Fatalf("output %q", out)
	}
	if want := []string{"https://dev/code", "https://dev/auth"}; !reflect.DeepEqual(*urls, want) {
		t.Fatalf("opened %v", *urls)
	}
	m := request(t, got)
	if m.Type != ipc.TypePluginConnect || m.ID != "c" {
		t.Fatal(m)
	}
	r := data[ipc.PluginConnect](t, m)
	if r.Plugin != "gh" || !reflect.DeepEqual(r.Params, map[string]string{"token": "a=b", "region": "eu"}) {
		t.Fatalf("%+v", r)
	}
}

func TestPluginConnectNoParams(t *testing.T) {
	stubOpen(t)
	got := fakeDaemon(t, func(c *ipc.Conn) { reply(c, ipc.TypePluginUpdated, "", state("gh", model.PluginConnected)) })
	if code, _, _ := runCtl("plugin", "connect", "gh"); code != 0 {
		t.Fatal(code)
	}
	m := request(t, got)
	if r := data[ipc.PluginConnect](t, m); r.Plugin != "gh" || len(r.Params) != 0 {
		t.Fatalf("%s", m.Data)
	}
}

func TestPluginConnectFailureStatuses(t *testing.T) {
	stubOpen(t)
	for _, st := range []string{model.PluginError, model.PluginExpired, model.PluginDisconnected} {
		fakeDaemon(t, func(c *ipc.Conn) { reply(c, ipc.TypePluginUpdated, "", state("gh", st)) })
		code, out, _ := runCtl("plugin", "connect", "gh")
		if code != 1 || !strings.Contains(out, st) {
			t.Fatalf("%s: %d %q", st, code, out)
		}
	}
}

func TestPluginConnectError(t *testing.T) {
	stubOpen(t)
	fakeDaemon(t, func(c *ipc.Conn) {
		reply(c, ipc.TypeError, "c", map[string]string{"message": "boom"})
	})
	code, out, errs := runCtl("plugin", "connect", "gh")
	if code != 1 || out != "" || !strings.Contains(errs, "boom") {
		t.Fatalf("%d %q %q", code, out, errs)
	}
}

func TestPluginConnectLost(t *testing.T) {
	stubOpen(t)
	fakeDaemon(t, func(c *ipc.Conn) { c.Close() })
	code, _, errs := runCtl("plugin", "connect", "gh")
	if code != 1 || errs == "" {
		t.Fatalf("%d %q", code, errs)
	}
}

var timeLine = regexp.MustCompile(`^\d\d:\d\d:\d\d\.\d{3} (\S+) (.*)$`)

func TestWatchPrintsEvents(t *testing.T) {
	got := fakeDaemon(t, func(c *ipc.Conn) {
		reply(c, ipc.TypeSessionUpdated, "", map[string]string{"id": "s1"})
		reply(c, ipc.TypeApprovalRequest, "x", model.Approval{ID: "ap1"})
		time.Sleep(100 * time.Millisecond)
		c.Close()
	})
	code, out, errs := runCtl("watch")
	if code != 0 || !strings.HasPrefix(errs, "disconnected: ") {
		t.Fatalf("%d %q", code, errs)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("%q", out)
	}
	var types []string
	for _, l := range lines {
		m := timeLine.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("line %q", l)
		}
		types = append(types, m[1])
	}
	if !reflect.DeepEqual(types, []string{"state.snapshot", "session.updated", "approval.request"}) {
		t.Fatal(types)
	}
	if !strings.HasSuffix(lines[1], `{"id":"s1"}`) {
		t.Fatalf("%q", lines[1])
	}
	next(t, got)   // hello
	noMore(t, got) // no -answer: nothing sent back
}

func TestWatchAnswer(t *testing.T) {
	for _, tc := range []struct {
		answer       string
		allowSession bool
		want         string
	}{
		{"allow_session", true, "allow_session"},
		{"allow_session", false, "allow"},
		{"deny", false, "deny"},
		{"allow", true, "allow"},
	} {
		got := fakeDaemon(t, func(c *ipc.Conn) {
			reply(c, ipc.TypeSessionUpdated, "", map[string]string{"id": "s1"}) // not answered
			reply(c, ipc.TypeApprovalRequest, "x", model.Approval{ID: "ap9", AllowSession: tc.allowSession})
			time.Sleep(200 * time.Millisecond)
			c.Close()
		})
		code, _, _ := runCtl("watch", "-answer", tc.answer)
		if code != 0 {
			t.Fatal(code)
		}
		m := request(t, got)
		if m.Type != ipc.TypeApprovalResponse || m.ID != "auto" {
			t.Fatalf("%+v", m)
		}
		if r := data[ipc.ApprovalResponse](t, m); r.ApprovalID != "ap9" || r.Answer != tc.want {
			t.Fatalf("%+v", r)
		}
		noMore(t, got)
	}
}
