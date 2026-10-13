package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/agentcfg"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/secret"
)

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeClaude is a minimal stand-in for the claude CLI: it answers
// --version and keeps the user-scope MCP server entry in
// $CLAUDE_CONFIG_DIR/.claude.json.
const fakeClaude = `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.287 (Claude Code)"; exit 0; fi
if [ "$1" = "mcp" ] && [ "$2" = "add-json" ]; then
  printf '{"mcpServers":{"%s":%s}}' "$3" "$4" > "$CLAUDE_CONFIG_DIR/.claude.json"
elif [ "$1" = "mcp" ] && [ "$2" = "remove" ]; then
  printf '{"mcpServers":{}}' > "$CLAUDE_CONFIG_DIR/.claude.json"
fi
exit 0
`

// sandbox points every farero path at short /tmp dirs and puts a fake
// claude CLI first on PATH. Nothing here touches the real home, Keychain or
// Claude Code config.
type sandbox struct {
	home, sock, claudeDir string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "frf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	sb := &sandbox{
		home:      filepath.Join(root, "h"),
		sock:      filepath.Join(root, "d.sock"),
		claudeDir: filepath.Join(root, "cc"),
	}
	bin := filepath.Join(root, "bin")
	for _, d := range []string{sb.claudeDir, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FARERO_HOME", sb.home)
	t.Setenv("FARERO_SOCKET", sb.sock)
	t.Setenv("FARERO_CLAUDE_CONFIG_DIR", sb.claudeDir)
	t.Setenv("FARERO_APPROVAL_TIMEOUT", "")
	t.Setenv("FARERO_SECRETS", "")
	t.Setenv("HOME", root) // a stray default path would land in the sandbox
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// registerAgentConfig exports CLAUDE_CONFIG_DIR; register a restore.
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return sb
}

type daemon struct {
	t      *testing.T
	sb     *sandbox
	log    *syncBuf
	cancel context.CancelFunc
	done   chan error
}

func (sb *sandbox) start(t *testing.T, dev bool) *daemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemon{t: t, sb: sb, log: &syncBuf{}, cancel: cancel, done: make(chan error, 1)}
	log := slog.New(slog.NewTextHandler(d.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	go func() { d.done <- run(ctx, log, dev) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-d.done:
		case <-time.After(10 * time.Second):
		}
	})
	return d
}

// stop cancels the daemon and returns run's result.
func (d *daemon) stop() error {
	d.t.Helper()
	d.cancel()
	select {
	case err := <-d.done:
		d.done <- err
		return err
	case <-time.After(10 * time.Second):
		d.t.Fatalf("farerod did not stop; log:\n%s", d.log)
		return nil
	}
}

// hello dials the socket (retrying while the daemon starts) and returns the
// connection with the first state.snapshot.
func (d *daemon) hello() (*ipc.Conn, ipc.StateSnapshot) {
	d.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := ipc.Dial(d.sb.sock, time.Second)
		if err == nil {
			conn.Send(ipc.TypeUIHello, "hello", ipc.UIHello{Version: "test"})
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			m, err := conn.Read()
			if err == nil && m.Type == ipc.TypeStateSnapshot {
				snap, _ := ipc.Decode[ipc.StateSnapshot](m)
				conn.SetDeadline(time.Time{})
				return conn, snap
			}
			conn.Close()
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("daemon never answered ui.hello; log:\n%s", d.log)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitGateway returns a connection and a snapshot with the gateway running.
func (d *daemon) waitGateway() (*ipc.Conn, ipc.StateSnapshot) {
	d.t.Helper()
	conn, snap := d.hello()
	deadline := time.Now().Add(10 * time.Second)
	for !snap.Gateway.Running {
		conn.SetDeadline(deadline)
		m, err := conn.Read()
		if err != nil {
			d.t.Fatalf("waiting for gateway: %v; log:\n%s", err, d.log)
		}
		if m.Type == ipc.TypeStateSnapshot {
			snap, _ = ipc.Decode[ipc.StateSnapshot](m)
		}
	}
	conn.SetDeadline(time.Time{})
	return conn, snap
}

// call sends a request and returns the reply with the same id, skipping
// server notifications.
func call(t *testing.T, conn *ipc.Conn, typ string, data any) ipc.Message {
	t.Helper()
	conn.Send(typ, "req", data)
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetDeadline(time.Time{})
	for {
		m, err := conn.Read()
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if m.ID == "req" {
			return m
		}
	}
}

func TestRunLifecycle(t *testing.T) {
	sb := newSandbox(t)
	d := sb.start(t, true)
	conn, snap := d.waitGateway()
	defer conn.Close()
	if snap.Version != version {
		t.Errorf("snapshot version = %q, want %q", snap.Version, version)
	}
	if snap.Gateway.Port == 0 || !strings.HasPrefix(snap.Gateway.URL, "http://127.0.0.1:") {
		t.Errorf("gateway = %+v", snap.Gateway)
	}
	fi, err := os.Stat(sb.sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %v", fi.Mode().Perm())
	}
	// --dev keeps secrets in a file under FARERO_HOME.
	if _, err := os.Stat(filepath.Join(sb.home, "dev-secrets.json")); err != nil {
		t.Errorf("dev secret store not used: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sb.home, "farero.db")); err != nil {
		t.Errorf("db: %v", err)
	}
	if err := d.stop(); err != nil {
		t.Fatalf("run = %v", err)
	}
	if _, err := os.Stat(sb.sock); !os.IsNotExist(err) {
		t.Errorf("socket not removed: %v", err)
	}
	out := d.log.String()
	for _, want := range []string{"farerod started", "dev=true", "gateway listening", "farerod stopped"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("errors logged:\n%s", out)
	}
	// The connection is closed with the daemon.
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, err := conn.Read(); err != nil {
			break
		}
	}
}

func TestRunReusesGatewayPort(t *testing.T) {
	sb := newSandbox(t)
	d := sb.start(t, true)
	c1, s1 := d.waitGateway()
	c1.Close()
	if err := d.stop(); err != nil {
		t.Fatal(err)
	}
	d2 := sb.start(t, true)
	c2, s2 := d2.waitGateway()
	defer c2.Close()
	if s1.Gateway.Port != s2.Gateway.Port {
		t.Errorf("port changed: %d -> %d", s1.Gateway.Port, s2.Gateway.Port)
	}
}

func TestRunApprovalTimeoutEnv(t *testing.T) {
	sb := newSandbox(t)
	t.Setenv("FARERO_APPROVAL_TIMEOUT", "90s")
	d := sb.start(t, true)
	conn, _ := d.waitGateway()
	conn.Close()
	if !strings.Contains(d.log.String(), "approval timeout") {
		t.Errorf("log lacks approval timeout:\n%s", d.log)
	}
}

func TestRunAnotherDaemonListening(t *testing.T) {
	sb := newSandbox(t)
	d := sb.start(t, true)
	conn, _ := d.waitGateway()
	defer conn.Close()
	var lg syncBuf
	err := run(context.Background(), slog.New(slog.NewTextHandler(&lg, nil)), true)
	if err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("second run = %v", err)
	}
	// The first daemon must still own its socket.
	if _, err := os.Stat(sb.sock); err != nil {
		t.Errorf("socket gone: %v", err)
	}
	c2, _ := d.hello()
	c2.Close()
}

func TestRunStartupErrors(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Run("support dir under a file", func(t *testing.T) {
		sb := newSandbox(t)
		file := filepath.Join(filepath.Dir(sb.home), "afile")
		os.WriteFile(file, nil, 0o600)
		t.Setenv("FARERO_HOME", filepath.Join(file, "sub"))
		if err := run(context.Background(), log, true); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("db is a directory", func(t *testing.T) {
		sb := newSandbox(t)
		os.MkdirAll(filepath.Join(sb.home, "farero.db", "x"), 0o700)
		err := run(context.Background(), log, true)
		if err == nil || !strings.Contains(err.Error(), "open db") {
			t.Fatalf("run = %v", err)
		}
	})
	t.Run("socket path too long", func(t *testing.T) {
		sb := newSandbox(t)
		t.Setenv("FARERO_SOCKET", filepath.Join(sb.home, strings.Repeat("s", 120)+".sock"))
		if err := run(context.Background(), log, true); err == nil {
			t.Fatal("expected error")
		}
	})
}

// A broken secret store must not stop farerod: the app and the hooks still
// connect and the snapshot carries the gateway error.
func TestRunGatewayFailureKeepsRunning(t *testing.T) {
	sb := newSandbox(t)
	os.MkdirAll(sb.home, 0o700)
	os.WriteFile(filepath.Join(sb.home, "dev-secrets.json"), []byte("{broken"), 0o600)
	d := sb.start(t, true)
	conn, snap := d.hello()
	defer conn.Close()
	deadline := time.Now().Add(10 * time.Second)
	for snap.Gateway.Error == "" {
		conn.SetDeadline(deadline)
		m, err := conn.Read()
		if err != nil {
			t.Fatalf("waiting for gateway error: %v\n%s", err, d.log)
		}
		if m.Type == ipc.TypeStateSnapshot {
			snap, _ = ipc.Decode[ipc.StateSnapshot](m)
		}
	}
	conn.SetDeadline(time.Time{})
	if snap.Gateway.Running || snap.Gateway.URL != "" {
		t.Errorf("gateway = %+v", snap.Gateway)
	}
	// The installer refuses to register without a gateway.
	m := call(t, conn, ipc.TypeAgentCfgApply, ipc.AgentRef{Agent: "claude"})
	if m.Type != ipc.TypeError || !strings.Contains(string(m.Data), "게이트웨이가 실행 중이 아님") {
		t.Errorf("apply = %s %s", m.Type, m.Data)
	}
	if !strings.Contains(d.log.String(), "gateway not started") {
		t.Errorf("log lacks gateway error:\n%s", d.log)
	}
	if err := d.stop(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentConfigOverIPC(t *testing.T) {
	sb := newSandbox(t)
	d := sb.start(t, true)
	conn, snap := d.waitGateway()
	defer conn.Close()

	m := call(t, conn, ipc.TypeAgentCfgStatus, ipc.AgentRef{Agent: "claude"})
	if m.Type != ipc.TypeAgentCfgStatus {
		t.Fatalf("status reply = %s %s", m.Type, m.Data)
	}
	st, _ := ipc.Decode[ipc.AgentCfgStatus](m)
	if !st.CLIFound || st.Version != "2.1.287" || !st.VersionOK {
		t.Errorf("status = %+v", st)
	}
	if st.HooksInstalled || st.MCPInstalled || st.AllowInstalled {
		t.Errorf("nothing should be installed yet: %+v", st)
	}
	if st.GatewayURL != snap.Gateway.URL {
		t.Errorf("gateway url = %q, want %q", st.GatewayURL, snap.Gateway.URL)
	}
	if st.SettingsPath != filepath.Join(sb.claudeDir, "settings.json") {
		t.Errorf("settings path = %q (config dir not isolated)", st.SettingsPath)
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if st.HookPath != filepath.Join(filepath.Dir(exe), "farero-hook") {
		t.Errorf("hook path = %q", st.HookPath)
	}
	// An empty agent means claude.
	if m := call(t, conn, ipc.TypeAgentCfgStatus, nil); m.Type != ipc.TypeAgentCfgStatus {
		t.Errorf("empty agent = %s %s", m.Type, m.Data)
	}

	m = call(t, conn, ipc.TypeAgentCfgPlan, ipc.AgentRef{Agent: "claude"})
	if m.Type != ipc.TypeAgentCfgPlan {
		t.Fatalf("plan reply = %s %s", m.Type, m.Data)
	}
	plan, _ := ipc.Decode[ipc.AgentCfgPlan](m)
	if len(plan.Changes) != 1 || len(plan.Commands) != 2 {
		t.Errorf("plan = %+v", plan)
	}

	m = call(t, conn, ipc.TypeAgentCfgApply, ipc.AgentRef{Agent: "claude"})
	if m.Type != ipc.TypeAgentCfgStatus {
		t.Fatalf("apply reply = %s %s", m.Type, m.Data)
	}
	st, _ = ipc.Decode[ipc.AgentCfgStatus](m)
	if !st.HooksInstalled || !st.AllowInstalled || !st.MCPInstalled || st.StalePath {
		t.Errorf("after apply: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(sb.claudeDir, "settings.json")); err != nil {
		t.Errorf("settings.json not written in the sandbox: %v", err)
	}

	m = call(t, conn, ipc.TypeAgentCfgRemove, ipc.AgentRef{Agent: "claude"})
	if m.Type != ipc.TypeAgentCfgStatus {
		t.Fatalf("remove reply = %s %s", m.Type, m.Data)
	}
	st, _ = ipc.Decode[ipc.AgentCfgStatus](m)
	if st.HooksInstalled || st.AllowInstalled || st.MCPInstalled {
		t.Errorf("after remove: %+v", st)
	}

	for _, typ := range []string{ipc.TypeAgentCfgStatus, ipc.TypeAgentCfgPlan, ipc.TypeAgentCfgApply, ipc.TypeAgentCfgRemove} {
		m := call(t, conn, typ, ipc.AgentRef{Agent: "codex"})
		if m.Type != ipc.TypeError || !strings.Contains(string(m.Data), `unsupported agent \"codex\"`) {
			t.Errorf("%s with codex = %s %s", typ, m.Type, m.Data)
		}
		m = call(t, conn, typ, json.RawMessage(`"not an object"`))
		if m.Type != ipc.TypeError {
			t.Errorf("%s with bad data = %s %s", typ, m.Type, m.Data)
		}
	}
}

// After the app moved, farerod rewrites the stale hook path once the
// gateway is up (Q63) and tells the app.
func TestAgentConfigFixesStalePath(t *testing.T) {
	sb := newSandbox(t)
	// Pre-install with a hook path that is not farerod's own.
	t.Setenv("CLAUDE_CONFIG_DIR", sb.claudeDir)
	old := agentcfg.NewClaude("/old/place/farero-hook", func() string { return "http://127.0.0.1:1/mcp" }, filepath.Join(sb.home, "backups"))
	if _, err := old.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	d := sb.start(t, true)
	// The fix runs in the background once the gateway is up; the
	// notification may precede our connection, so wait for the log line and
	// ask for the status.
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(d.log.String(), "agent config paths updated") {
		if time.Now().After(deadline) {
			t.Fatalf("path never fixed:\n%s", d.log)
		}
		time.Sleep(20 * time.Millisecond)
	}
	conn, _ := d.hello()
	defer conn.Close()
	m := call(t, conn, ipc.TypeAgentCfgStatus, nil)
	fixed := &ipc.AgentCfgStatus{}
	*fixed, _ = ipc.Decode[ipc.AgentCfgStatus](m)
	if fixed.StalePath || !fixed.HooksInstalled || !fixed.MCPInstalled {
		t.Errorf("after fix: %+v", fixed)
	}
	if !strings.Contains(fixed.Message, "경로를 새 위치로 고쳤습니다") {
		t.Errorf("message = %q", fixed.Message)
	}
	b, _ := os.ReadFile(filepath.Join(sb.claudeDir, "settings.json"))
	if strings.Contains(string(b), "/old/place") {
		t.Errorf("settings.json still has the old path:\n%s", b)
	}
	if !strings.Contains(d.log.String(), "agent config paths updated") {
		t.Errorf("log:\n%s", d.log)
	}
}

// Without FARERO_CLAUDE_CONFIG_DIR a --dev run must not touch the real
// Claude Code config, so the automatic path fix is skipped.
func TestAgentConfigDevWithoutIsolationSkipsFix(t *testing.T) {
	sb := newSandbox(t)
	t.Setenv("FARERO_CLAUDE_CONFIG_DIR", "")
	// The default config dir resolves under the sandbox HOME.
	home := os.Getenv("HOME")
	cfg := filepath.Join(home, ".claude")
	os.MkdirAll(cfg, 0o755)
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	pre := agentcfg.NewClaude("/old/place/farero-hook", func() string { return "http://127.0.0.1:1/mcp" }, filepath.Join(sb.home, "backups"))
	if _, err := pre.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(cfg, "settings.json"))
	d := sb.start(t, true)
	conn, _ := d.waitGateway()
	conn.Close()
	time.Sleep(300 * time.Millisecond) // give the fixer goroutine time to (not) run
	after, _ := os.ReadFile(filepath.Join(cfg, "settings.json"))
	if !bytes.Equal(before, after) {
		t.Errorf("settings.json changed in --dev without isolation")
	}
	if strings.Contains(d.log.String(), "agent config paths updated") {
		t.Errorf("fix ran:\n%s", d.log)
	}
}

func TestMainVersionFlag(t *testing.T) {
	withArgs(t, "farerod", "-version")
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	main()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	if strings.TrimSpace(string(out)) != version {
		t.Errorf("stdout = %q, want %q", out, version)
	}
}

func TestMainStopsOnSIGTERM(t *testing.T) {
	sb := newSandbox(t)
	withArgs(t, "farerod", "-dev", "-debug")
	done := make(chan struct{})
	go func() { main(); close(done) }()
	d := &daemon{t: t, sb: sb, log: &syncBuf{}}
	conn, _ := d.hello()
	conn.Close()
	syscall.Kill(os.Getpid(), syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("main did not return after SIGTERM")
	}
	if _, err := os.Stat(sb.sock); !os.IsNotExist(err) {
		t.Errorf("socket left behind: %v", err)
	}
}

// withArgs gives main() a fresh flag set and os.Args for one test.
func withArgs(t *testing.T, args ...string) {
	t.Helper()
	oldArgs, oldFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFlags })
	os.Args = args
	flag.CommandLine = flag.NewFlagSet(args[0], flag.ContinueOnError)
}

// A failing run exits 1 and logs "farerod exiting". main calls os.Exit, so
// this runs the test binary again as farerod.
func TestMainExitCodeOnError(t *testing.T) {
	if os.Getenv("FARERO_TEST_AS_FAREROD") == "1" {
		os.Args = []string{"farerod", "-dev"}
		main()
		return
	}
	sb := newSandbox(t)
	file := filepath.Join(filepath.Dir(sb.home), "afile")
	os.WriteFile(file, nil, 0o600)
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainExitCodeOnError$")
	cmd.Env = append(os.Environ(), "FARERO_TEST_AS_FAREROD=1", "FARERO_HOME="+filepath.Join(file, "sub"))
	out, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 1 {
		t.Fatalf("err = %v, want exit status 1; output:\n%s", err, out)
	}
	if !strings.Contains(string(out), "farerod exiting") {
		t.Errorf("output lacks the exit log:\n%s", out)
	}
}

func TestApprovalTimeout(t *testing.T) {
	var buf syncBuf
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cases := []struct {
		name string
		env  string
		dev  bool
		want time.Duration
		logs string
	}{
		{"unset", "", true, 0, ""},
		{"valid in dev", "250ms", true, 250 * time.Millisecond, "approval timeout"},
		{"ignored outside dev", "250ms", false, 0, ""},
		{"unparsable", "soon", true, 0, "ignoring FARERO_APPROVAL_TIMEOUT"},
		{"zero", "0s", true, 0, "ignoring FARERO_APPROVAL_TIMEOUT"},
		{"negative", "-5s", true, 0, "ignoring FARERO_APPROVAL_TIMEOUT"},
		{"one nanosecond", "1ns", true, time.Nanosecond, "approval timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("FARERO_APPROVAL_TIMEOUT", c.env)
			buf = syncBuf{}
			if got := approvalTimeout(log, c.dev); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
			out := buf.String()
			if c.logs == "" && out != "" {
				t.Errorf("unexpected log: %s", out)
			}
			if c.logs != "" && !strings.Contains(out, c.logs) {
				t.Errorf("log %q lacks %q", out, c.logs)
			}
		})
	}
}

func TestLogOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logPath := filepath.Join(home, "Library", "Logs", "Farero", "farerod.log")

	t.Run("not under launchd", func(t *testing.T) {
		t.Setenv("XPC_SERVICE_NAME", "something.else")
		if w := logOutput(); w != os.Stderr {
			t.Errorf("got %v, want stderr", w)
		}
		if _, err := os.Stat(logPath); err == nil {
			t.Error("log file created outside launchd")
		}
	})

	t.Run("launchd writes a private log file", func(t *testing.T) {
		t.Setenv("XPC_SERVICE_NAME", launchAgentLabel)
		w := logOutput()
		f, ok := w.(*os.File)
		if !ok || f == os.Stderr {
			t.Fatalf("writer = %T", w)
		}
		io.WriteString(f, "first\n")
		f.Close()
		fi, err := os.Stat(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("log mode = %v", fi.Mode().Perm())
		}
		di, _ := os.Stat(filepath.Dir(logPath))
		if di.Mode().Perm() != 0o700 {
			t.Errorf("log dir mode = %v", di.Mode().Perm())
		}
		// A second start appends.
		f2 := logOutput().(*os.File)
		io.WriteString(f2, "second\n")
		f2.Close()
		if b, _ := os.ReadFile(logPath); string(b) != "first\nsecond\n" {
			t.Errorf("log = %q", b)
		}
	})

	t.Run("rotation at 10 MB", func(t *testing.T) {
		t.Setenv("XPC_SERVICE_NAME", launchAgentLabel)
		const limit = 10 << 20
		os.Truncate(logPath, limit) // exactly at the limit: kept
		f := logOutput().(*os.File)
		io.WriteString(f, "x")
		f.Close()
		if fi, _ := os.Stat(logPath); fi.Size() != limit+1 {
			t.Errorf("size at limit = %d, want %d", fi.Size(), limit+1)
		}
		// One byte over: kept as farerod.log.1, and a new file starts.
		f = logOutput().(*os.File)
		io.WriteString(f, "y")
		f.Close()
		if fi, _ := os.Stat(logPath); fi.Size() != 1 {
			t.Errorf("size over limit = %d, want 1", fi.Size())
		}
		if fi, err := os.Stat(logPath + ".1"); err != nil || fi.Size() != limit+1 {
			t.Errorf("previous log: %v, size %d", err, fi.Size())
		}
		// The next rotation replaces the old .1.
		os.Truncate(logPath, limit+2)
		f = logOutput().(*os.File)
		f.Close()
		if fi, _ := os.Stat(logPath + ".1"); fi.Size() != limit+2 {
			t.Errorf("replaced .1 size = %d, want %d", fi.Size(), limit+2)
		}
	})

	t.Run("falls back to stderr", func(t *testing.T) {
		t.Setenv("XPC_SERVICE_NAME", launchAgentLabel)
		// No home directory.
		t.Setenv("HOME", "")
		if w := logOutput(); w != os.Stderr {
			t.Errorf("no home: %v", w)
		}
		// Logs dir cannot be created: "Library" is a regular file.
		h2 := t.TempDir()
		os.WriteFile(filepath.Join(h2, "Library"), nil, 0o600)
		t.Setenv("HOME", h2)
		if w := logOutput(); w != os.Stderr {
			t.Errorf("mkdir fails: %v", w)
		}
		// The log file cannot be opened: a directory sits at its path.
		h3 := t.TempDir()
		os.MkdirAll(filepath.Join(h3, "Library", "Logs", "Farero", "farerod.log"), 0o700)
		t.Setenv("HOME", h3)
		if w := logOutput(); w != os.Stderr {
			t.Errorf("open fails: %v", w)
		}
	})
}

func TestSecretStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FARERO_HOME", dir)
	t.Setenv("FARERO_SECRETS", "")

	// Dev mode: the file store under FARERO_HOME.
	s := secretStore(true)
	if err := s.Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dev-secrets.json")); err != nil {
		t.Errorf("dev store file: %v", err)
	}
	if _, ok := s.(*secret.FileStore); !ok {
		t.Errorf("dev store is %T", s)
	}

	// FARERO_SECRETS=file selects it without --dev.
	t.Setenv("FARERO_SECRETS", "file")
	s = secretStore(false)
	if v, err := s.Get("k"); err != nil || v != "v" {
		t.Errorf("env file store: %q, %v", v, err)
	}

	// Any other value keeps the real store, which these tests never open.
	t.Setenv("FARERO_SECRETS", "keychain")
	if _, ok := secretStore(false).(*secret.FileStore); ok && keychainIsReal {
		t.Error("FARERO_SECRETS=keychain must not select the file store")
	}
}

func TestVersionDefault(t *testing.T) {
	if version != "dev" {
		t.Errorf("version = %q", version)
	}
	if keychainService != "dev.farero.farerod" || launchAgentLabel != "dev.farero.farerod" {
		t.Error("service names changed")
	}
	if githubClientID != "" {
		t.Errorf("githubClientID = %q", githubClientID)
	}
}
