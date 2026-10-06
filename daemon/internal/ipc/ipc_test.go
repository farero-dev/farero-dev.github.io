package ipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "frt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func TestRoundTripAndPermissions(t *testing.T) {
	path := sockPath(t)
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket mode %o, want 600", perm)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, l, func(ctx context.Context, c *Conn, first Message) {
		ev, _ := Decode[HookEvent](first)
		c.Send(TypeHookAck, first.ID, map[string]string{"echo": ev.Agent, "tty": ev.TTY})
	})

	c, err := Dial(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	big := strings.Repeat("가", 200_000) // longer than the bufio buffer
	if err := c.Send(TypeHookEvent, "h1", HookEvent{Agent: "claude", TTY: big}); err != nil {
		t.Fatal(err)
	}
	reply, err := c.Read()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Decode[map[string]string](reply)
	if reply.Type != TypeHookAck || reply.ID != "h1" || got["echo"] != "claude" || got["tty"] != big {
		t.Fatalf("reply %s %s %q", reply.Type, reply.ID, got["echo"])
	}
}

func TestListenRefusesLiveSocketAndReplacesStale(t *testing.T) {
	path := sockPath(t)
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(path); err == nil {
		t.Fatal("second Listen on a live socket must fail")
	}
	l.Close()
	os.WriteFile(path, nil, 0o600) // stale file left behind
	l2, err := Listen(path)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	l2.Close()
}

// Tool inputs reach the audit log through the socket: `&&` and `>>` must
// arrive as they are, not as & or >, or a log search misses them.
func TestNoHTMLEscaping(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	in := json.RawMessage(`{"tool_input":{"command":"npm i && echo ok >> log <x>"}}`)
	go NewConn(a).Send(TypeHookEvent, "1", HookEvent{Agent: "claude", Input: in, TTY: "a&b"})
	line, err := bufio.NewReader(b).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(line, []byte(`\u00`)) || !bytes.Contains(line, in) || !bytes.Contains(line, []byte(`"a&b"`)) {
		t.Fatalf("line: %s", line)
	}
}
