package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// M1 completion criterion: when farerod is not running, farero-hook ends at
// once, exit 0 and no decision, so Claude Code is never blocked and a
// PermissionRequest falls back to its own prompt.
func TestHookExitsAtOnceWithoutDaemon(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "farero-hook")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir, err := os.MkdirTemp("/tmp", "frh") // short: socket paths are limited to 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	// A socket file left behind by a farerod that died: nothing accepts.
	stale := filepath.Join(dir, "stale.sock")
	l, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()

	permission := `{"hook_event_name":"PermissionRequest","session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"}}`
	cases := []struct {
		name, socket, stdin string
		args                []string
		wantOut             string
	}{
		{"no socket, PermissionRequest", filepath.Join(dir, "missing.sock"), permission, []string{"--agent", "claude"}, ""},
		{"stale socket, PermissionRequest", stale, permission, []string{"--agent", "claude"}, ""},
		{"no socket, PreToolUse", filepath.Join(dir, "missing.sock"), `{"hook_event_name":"PreToolUse","session_id":"s1"}`, []string{"--agent", "claude"}, ""},
		// headersHelper must still print a JSON object.
		{"no socket, headers", filepath.Join(dir, "missing.sock"), "", []string{"--headers", "--agent", "claude"}, "{}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bin, tc.args...)
			cmd.Env = append(os.Environ(), "FARERO_SOCKET="+tc.socket)
			cmd.Stdin = strings.NewReader(tc.stdin)
			start := time.Now()
			out, err := cmd.Output()
			took := time.Since(start)
			if err != nil {
				t.Fatalf("exit: %v", err)
			}
			if got := strings.TrimSpace(string(out)); got != tc.wantOut {
				t.Errorf("stdout = %q, want %q", got, tc.wantOut)
			}
			if took > time.Second {
				t.Errorf("took %v", took)
			}
		})
	}
}

func TestAgentPID(t *testing.T) {
	procs := map[int]struct {
		comm string
		ppid int
	}{
		500: {"/bin/zsh", 400},
		400: {"claude", 300},
		600: {"-bash", 1},
	}
	info := func(pid int) (string, int) { p := procs[pid]; return p.comm, p.ppid }
	if got := agentPID(400, info); got != 400 {
		t.Fatalf("direct child: %d", got)
	}
	if got := agentPID(500, info); got != 400 {
		t.Fatalf("through a shell: %d", got)
	}
	// claude exited before the hook: launchd (1) adopted it or its shell.
	if got := agentPID(1, info); got != 0 {
		t.Fatalf("orphan: %d", got)
	}
	if got := agentPID(600, info); got != 0 {
		t.Fatalf("orphan through a shell: %d", got)
	}
}
