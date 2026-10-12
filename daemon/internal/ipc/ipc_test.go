package ipc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// readOne feeds raw bytes to a Conn and returns what Read makes of them.
func readOne(t *testing.T, payload func(w io.Writer)) (Message, error) {
	t.Helper()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		payload(b)
		b.Close()
	}()
	return NewConn(a).Read()
}

// A line of exactly MaxLine bytes is accepted, one byte more is refused.
func TestReadLineSizeLimit(t *testing.T) {
	const head, tail = `{"type":"t","data":"`, `"}`
	line := func(n int) func(io.Writer) {
		return func(w io.Writer) {
			pad := n - len(head) - len(tail)
			w.Write([]byte(head))
			chunk := bytes.Repeat([]byte("x"), 1<<20)
			for pad > 0 {
				k := min(pad, len(chunk))
				w.Write(chunk[:k])
				pad -= k
			}
			w.Write([]byte(tail + "\n"))
		}
	}
	m, err := readOne(t, line(MaxLine))
	if err != nil || m.Type != "t" || len(m.Data) != MaxLine-len(head)-len(tail)+2 {
		t.Fatalf("MaxLine line: type=%q data=%d err=%v", m.Type, len(m.Data), err)
	}
	if _, err := readOne(t, line(MaxLine+1)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("MaxLine+1 line: err=%v", err)
	}
}

// fakeListener replays scripted Accept errors, then blocks until closed.
type fakeListener struct {
	errs   []error
	closed chan struct{}
	once   sync.Once
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func (l *fakeListener) Accept() (net.Conn, error) {
	if len(l.errs) > 0 {
		err := l.errs[0]
		l.errs = l.errs[1:]
		return nil, err
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *fakeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *fakeListener) Addr() net.Addr { return &net.UnixAddr{Name: "fake", Net: "unix"} }

// Serve ends quietly when its context is cancelled, but a real Accept
// failure on a live context is reported, and timeouts are retried.
func TestServeStopsOnContextAndReportsAcceptErrors(t *testing.T) {
	h := func(context.Context, *Conn, Message) {}
	wait := func(ch chan error) error {
		select {
		case err := <-ch:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("Serve did not return")
			return nil
		}
	}

	// Cancelled context: nil.
	l := &fakeListener{closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, l, h) }()
	cancel()
	if err := wait(done); err != nil {
		t.Fatalf("Serve after cancel = %v, want nil", err)
	}

	// Timeouts are skipped, then a hard error is returned as is.
	boom := errors.New("boom")
	l = &fakeListener{errs: []error{timeoutErr{}, timeoutErr{}, boom}, closed: make(chan struct{})}
	go func() { done <- Serve(context.Background(), l, h) }()
	if err := wait(done); err != boom {
		t.Fatalf("Serve = %v, want %v", err, boom)
	}
}
