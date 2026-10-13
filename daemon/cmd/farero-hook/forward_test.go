package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/ipc"
)

// fakeDaemon answers on a short socket under /tmp (FARERO_SOCKET) and
// hands every first message it gets to the test.
func fakeDaemon(t *testing.T, reply func(c *ipc.Conn, first ipc.Message)) chan ipc.Message {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "frh") // socket paths are limited to 104 bytes
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
	got := make(chan ipc.Message, 4)
	go ipc.Serve(ctx, l, func(_ context.Context, c *ipc.Conn, first ipc.Message) {
		got <- first
		reply(c, first)
	})
	t.Setenv("FARERO_SOCKET", sock)
	return got
}

// runWith runs f with stdin reading in and returns what f wrote to stdout.
func runWith(t *testing.T, in string, f func()) string {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		io.WriteString(inW, in)
		inW.Close()
	}()
	out := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(outR)
		out <- string(b)
	}()
	stdin, stdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	defer func() { os.Stdin, os.Stdout = stdin, stdout }()
	f()
	outW.Close()
	return <-out
}

func decode(t *testing.T, s string) any {
	t.Helper()
	if s == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("not JSON: %q", s)
	}
	return v
}

const permissionInput = `{"hook_event_name":"PermissionRequest","session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"}}`

// The daemon's answer becomes Claude Code's PermissionRequest output; no
// decision, or a reply of another type, prints nothing.
func TestHookPrintsDaemonDecision(t *testing.T) {
	allow := map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PermissionRequest", "decision": map[string]any{"behavior": "allow"}}}
	deny := map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PermissionRequest", "decision": map[string]any{"behavior": "deny", "message": "farero: 사용자가 거부함"}}}
	for _, tc := range []struct {
		name  string
		typ   string
		reply any
		want  any
	}{
		{"allow", ipc.TypeHookDecision, ipc.HookDecision{Behavior: ipc.BehaviorAllow}, allow},
		{"deny", ipc.TypeHookDecision, ipc.HookDecision{Behavior: ipc.BehaviorDeny, Reason: "사용자가 거부함"}, deny},
		{"no decision", ipc.TypeHookDecision, ipc.HookDecision{Behavior: ipc.BehaviorNone}, nil},
		{"ack only", ipc.TypeHookAck, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := fakeDaemon(t, func(c *ipc.Conn, first ipc.Message) { c.Send(tc.typ, first.ID, tc.reply) })
			out := runWith(t, permissionInput, func() { runHook("claude", 4242) })
			if d := decode(t, out); !reflect.DeepEqual(d, tc.want) {
				t.Fatalf("stdout %q, want %v", out, tc.want)
			}
			m := <-got
			ev, err := ipc.Decode[ipc.HookEvent](m)
			if m.Type != ipc.TypeHookEvent || err != nil || ev.Agent != "claude" || ev.PID != 4242 || string(ev.Input) != permissionInput {
				t.Fatalf("daemon got %s %+v %v", m.Type, ev, err)
			}
		})
	}
}

// A PermissionRequest waits for the user's answer; other events give up
// after ackTimeout so the agent is not held up.
func TestHookWaitsOnlyForPermissionRequest(t *testing.T) {
	late := func(c *ipc.Conn, first ipc.Message) {
		time.Sleep(ackTimeout + 300*time.Millisecond)
		c.Send(ipc.TypeHookDecision, first.ID, ipc.HookDecision{Behavior: ipc.BehaviorAllow})
	}
	fakeDaemon(t, late)
	if out := runWith(t, permissionInput, func() { runHook("claude", 0) }); out == "" {
		t.Fatal("PermissionRequest gave up before the answer")
	}
	start := time.Now()
	out := runWith(t, `{"hook_event_name":"PreToolUse","session_id":"s1"}`, func() { runHook("claude", 0) })
	if out != "" || time.Since(start) > ackTimeout+200*time.Millisecond {
		t.Fatalf("PreToolUse: %q after %v", out, time.Since(start))
	}
}

// Empty stdin is not sent to the daemon.
func TestHookIgnoresEmptyInput(t *testing.T) {
	got := fakeDaemon(t, func(c *ipc.Conn, first ipc.Message) {})
	if out := runWith(t, "", func() { runHook("claude", 0) }); out != "" {
		t.Fatalf("stdout %q", out)
	}
	select {
	case m := <-got:
		t.Fatalf("daemon got %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// headersHelper prints the headers the daemon issues for this agent
// process, and {} when the reply is not a headers result.
func TestHeadersPrintsDaemonHeaders(t *testing.T) {
	headers := map[string]string{"Authorization": "Bearer s3cret", "X-Farero-Conn": "4242.abc"}
	for _, tc := range []struct {
		name  string
		typ   string
		reply any
		want  map[string]string
	}{
		{"issued", ipc.TypeHeadersResult, ipc.HeadersResult{Headers: headers}, headers},
		{"no headers", ipc.TypeHeadersResult, ipc.HeadersResult{}, map[string]string{}},
		{"other reply", ipc.TypeHookAck, nil, map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := fakeDaemon(t, func(c *ipc.Conn, first ipc.Message) { c.Send(tc.typ, first.ID, tc.reply) })
			out := runWith(t, "", func() { runHeaders("claude", 4242) })
			var printed map[string]string
			if err := json.Unmarshal([]byte(out), &printed); err != nil || !reflect.DeepEqual(printed, tc.want) {
				t.Fatalf("stdout %q (%v), want %v", out, err, tc.want)
			}
			m := <-got
			if r, err := ipc.Decode[ipc.HeadersIssue](m); m.Type != ipc.TypeHeadersIssue || err != nil || r.Agent != "claude" || r.PID != 4242 {
				t.Fatalf("daemon got %s %+v %v", m.Type, r, err)
			}
		})
	}
}

// agentPID walks up through at most three shells, login shells included.
func TestAgentPIDThroughShells(t *testing.T) {
	procs := map[int]struct {
		comm string
		ppid int
	}{
		700: {"/bin/sh", 701}, 701: {"bash", 702}, 702: {"-zsh", 703}, 703: {"/usr/local/bin/claude", 300},
		800: {"dash", 801}, 801: {"fish", 802}, 802: {"sh", 803}, 803: {"sh", 804}, 804: {"claude", 300},
	}
	info := func(pid int) (string, int) { p := procs[pid]; return p.comm, p.ppid }
	if got := agentPID(700, info); got != 703 {
		t.Fatalf("three shells: %d", got)
	}
	if got := agentPID(800, info); got != 803 {
		t.Fatalf("four shells stop after three: %d", got)
	}
}
