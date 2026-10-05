// farero-hook is run by the agent for every hook event and as the gateway
// headersHelper (아키텍처 5장).
//
//	farero-hook --agent claude     hook mode: stdin is the hook input JSON
//	farero-hook --headers          headersHelper mode: prints gateway headers
//
// If farerod is not reachable it exits 0 without output so the agent is
// never blocked (a PermissionRequest then falls back to the agent's prompt).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/paths"
)

// version is set at build time.
var version = "dev"

const (
	dialTimeout = 300 * time.Millisecond
	ackTimeout  = 3 * time.Second
	// Claude Code kills the hook after the entry's timeout (660 s, Q52);
	// farerod answers deny at the 10 minute approval deadline before that.
	decisionTimeout = 11 * time.Minute
)

func main() {
	agent := flag.String("agent", "claude", "agent kind")
	headers := flag.Bool("headers", false, "headersHelper mode")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	agentPID := agentProcess()
	if *headers {
		runHeaders(*agent, agentPID)
		return
	}
	runHook(*agent, agentPID)
}

func runHook(agent string, pid int) {
	in, err := io.ReadAll(io.LimitReader(os.Stdin, ipc.MaxLine))
	if err != nil || len(in) == 0 {
		return
	}
	var probe struct {
		Event string `json:"hook_event_name"`
	}
	_ = json.Unmarshal(in, &probe)

	conn, err := ipc.Dial(paths.Socket(), dialTimeout)
	if err != nil {
		return
	}
	defer conn.Close()
	wait := ackTimeout
	if probe.Event == "PermissionRequest" {
		wait = decisionTimeout
	}
	conn.SetDeadline(time.Now().Add(wait))
	if err := conn.Send(ipc.TypeHookEvent, "1", ipc.HookEvent{Agent: agent, Input: in, TTY: ttyOf(pid), PID: pid}); err != nil {
		return
	}
	reply, err := conn.Read()
	if err != nil || reply.Type != ipc.TypeHookDecision {
		return
	}
	d, err := ipc.Decode[ipc.HookDecision](reply)
	if err != nil {
		return
	}
	switch d.Behavior {
	case ipc.BehaviorAllow:
		printDecision(map[string]any{"behavior": "allow"})
	case ipc.BehaviorDeny:
		printDecision(map[string]any{"behavior": "deny", "message": "farero: " + d.Reason})
	}
}

// printDecision writes Claude Code's PermissionRequest output.
func printDecision(decision map[string]any) {
	json.NewEncoder(os.Stdout).Encode(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest",
			"decision":      decision,
		},
	})
}

func runHeaders(agent string, pid int) {
	out := map[string]string{}
	defer func() { json.NewEncoder(os.Stdout).Encode(out) }()
	conn, err := ipc.Dial(paths.Socket(), dialTimeout)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(ackTimeout))
	if conn.Send(ipc.TypeHeadersIssue, "1", ipc.HeadersIssue{Agent: agent, PID: pid}) != nil {
		return
	}
	reply, err := conn.Read()
	if err != nil || reply.Type != ipc.TypeHeadersResult {
		return
	}
	if r, err := ipc.Decode[ipc.HeadersResult](reply); err == nil && r.Headers != nil {
		out = r.Headers
	}
}

// agentProcess returns the agent's PID: our parent, or its parent when the
// command was started through a shell.
func agentProcess() int {
	pid := os.Getppid()
	for i := 0; i < 3 && pid > 1; i++ {
		comm, ppid := psInfo(pid)
		base := comm[strings.LastIndex(comm, "/")+1:]
		base = strings.TrimPrefix(base, "-")
		switch base {
		case "sh", "bash", "zsh", "dash", "fish":
			pid = ppid
			continue
		}
		break
	}
	return pid
}

func psInfo(pid int) (comm string, ppid int) {
	out, err := exec.Command("/bin/ps", "-o", "ppid=,comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", 0
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return "", 0
	}
	ppid, _ = strconv.Atoi(fields[0])
	return strings.Join(fields[1:], " "), ppid
}

// ttyOf returns the controlling terminal of pid ("ttys003"), or "".
func ttyOf(pid int) string {
	out, err := exec.Command("/bin/ps", "-o", "tty=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	t := strings.TrimSpace(string(out))
	if t == "" || t == "??" {
		return ""
	}
	return t
}
