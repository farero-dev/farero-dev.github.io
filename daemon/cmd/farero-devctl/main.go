// farero-devctl is a development stand-in for the app: it connects to
// farerod as a UI client, prints every event, and can answer approval
// cards automatically. It is not shipped in the app bundle.
//
//	farero-devctl watch [-answer allow|allow_session|deny]
//	farero-devctl answer <approval id> allow|allow_session|deny
//	farero-devctl log [-q text] [-n 20]
//	farero-devctl policy [set <plugin> <tool> [auto|ask|block]]  (no level: back to the default)
//	farero-devctl agentcfg status|plan|apply|remove
//	farero-devctl plugin [connect <plugin> [key=value…] | disconnect <plugin> | option <plugin> <key> <value> | tools <plugin>]
//	farero-devctl settings [limit <bytes> | update on|off]
//	farero-devctl session delete <session id>
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/paths"
	"github.com/farero-dev/farero/daemon/internal/store"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// devctl carries the output streams and the browser opener so tests can
// run every subcommand in process.
type devctl struct {
	out, errw io.Writer
	open      func(url string)
}

// openURL opens a URL with the macOS open command; tests replace it.
var openURL = func(url string) { exec.Command("open", url).Run() }

// run executes one farero-devctl invocation and returns its exit code.
func run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	d := &devctl{out: stdout, errw: stderr, open: openURL}
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: farero-devctl watch|answer|log|policy|agentcfg|plugin|settings|session")
		return 2
	}
	cmd, args := args[0], args[1:]
	conn, err := ipc.Dial(paths.Socket(), time.Second)
	if err != nil {
		fmt.Fprintln(stderr, "farerod not reachable:", err)
		return 1
	}
	defer conn.Close()
	conn.Send(ipc.TypeUIHello, "hello", ipc.UIHello{Version: "devctl"})
	snap, err := conn.Read()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch cmd {
	case "watch":
		fs := flag.NewFlagSet("watch", flag.ContinueOnError)
		fs.SetOutput(stderr)
		answer := fs.String("answer", "", "answer approvals automatically")
		if code, done := parseFlags(fs, args); done {
			return code
		}
		fmt.Fprintln(stdout, line(snap))
		for {
			m, err := conn.Read()
			if err != nil {
				fmt.Fprintln(stderr, "disconnected:", err)
				return 0
			}
			fmt.Fprintln(stdout, line(m))
			if m.Type == ipc.TypeApprovalRequest && *answer != "" {
				a, _ := ipc.Decode[model.Approval](m)
				ans := *answer
				if ans == model.AnswerAllowSession && !a.AllowSession {
					ans = model.AnswerAllow
				}
				conn.Send(ipc.TypeApprovalResponse, "auto", ipc.ApprovalResponse{ApprovalID: a.ID, Answer: ans})
			}
		}
	case "answer":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: farero-devctl answer <approval id> allow|allow_session|deny")
			return 2
		}
		conn.Send(ipc.TypeApprovalResponse, "r", ipc.ApprovalResponse{ApprovalID: args[0], Answer: args[1]})
		return d.printReply(conn, "r")
	case "log":
		fs := flag.NewFlagSet("log", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if len(args) == 1 && args[0] == "delete-unknown" {
			conn.Send(ipc.TypeLogDeleteUnknown, "u", nil)
			return d.printReply(conn, "u")
		}
		q := fs.String("q", "", "search text")
		n := fs.Int("n", 20, "rows")
		if code, done := parseFlags(fs, args); done {
			return code
		}
		conn.Send(ipc.TypeLogQuery, "q", store.CallFilter{Query: *q, Limit: *n})
		return d.printReply(conn, "q")
	case "policy":
		if len(args) == 0 {
			conn.Send(ipc.TypePolicyGet, "p", nil)
			return d.printReply(conn, "p")
		}
		if args[0] != "set" || len(args) < 3 || len(args) > 4 {
			fmt.Fprintln(stderr, "usage: farero-devctl policy [set <plugin> <tool> [auto|ask|block]]")
			return 2
		}
		level := ""
		if len(args) == 4 {
			level = args[3]
		}
		conn.Send(ipc.TypePolicySet, "p", ipc.PolicySet{Plugin: args[1], Tool: args[2], Level: level})
		return d.printReply(conn, "p")
	case "agentcfg":
		op := "status"
		if len(args) > 0 {
			op = args[0]
		}
		conn.Send("agentcfg."+op, "a", ipc.AgentRef{Agent: "claude"})
		return d.printReply(conn, "a")
	case "plugin":
		return d.pluginCmd(conn, args)
	case "settings":
		return d.settingsCmd(conn, args)
	case "session":
		if len(args) != 2 || args[0] != "delete" {
			fmt.Fprintln(stderr, "usage: farero-devctl session delete <session id>")
			return 2
		}
		conn.Send(ipc.TypeSessionDelete, "s", ipc.SessionRef{SessionID: args[1]})
		return d.printReply(conn, "s")
	default:
		fmt.Fprintln(stderr, "unknown command", cmd)
		return 2
	}
}

// parseFlags parses a subcommand's flags like flag.ExitOnError did: done
// is true when the command must stop with code (0 for -h, 2 otherwise).
func parseFlags(fs *flag.FlagSet, args []string) (code int, done bool) {
	err := fs.Parse(args)
	if err == nil {
		return 0, false
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0, true
	}
	return 2, true
}

const settingsUsage = "usage: farero-devctl settings [limit <bytes> | update on|off]"

// settingsCmd prints the settings, or changes one of them (settings.set
// leaves the other as it is).
func (d *devctl) settingsCmd(conn *ipc.Conn, args []string) int {
	if len(args) == 0 {
		conn.Send(ipc.TypeSettingsGet, "g", nil)
		return d.printReply(conn, "g")
	}
	if len(args) != 2 {
		fmt.Fprintln(d.errw, settingsUsage)
		return 2
	}
	var req ipc.SettingsSet
	switch args[0] {
	case "limit":
		n, err := strconv.Atoi(args[1])
		if err != nil {
			fmt.Fprintln(d.errw, settingsUsage)
			return 2
		}
		req.ResultLimitBytes = &n
	case "update":
		on := args[1] == "on"
		if !on && args[1] != "off" {
			fmt.Fprintln(d.errw, settingsUsage)
			return 2
		}
		req.UpdateCheck = &on
	default:
		fmt.Fprintln(d.errw, settingsUsage)
		return 2
	}
	conn.Send(ipc.TypeSettingsSet, "s", req)
	return d.printReply(conn, "s")
}

const pluginUsage = "usage: farero-devctl plugin [connect <plugin> [key=value…] | disconnect <plugin> | option <plugin> <key> <value> | tools <plugin>]"

func (d *devctl) pluginCmd(conn *ipc.Conn, args []string) int {
	if len(args) == 0 {
		conn.Send(ipc.TypePluginList, "l", nil)
		return d.printReply(conn, "l")
	}
	if len(args) < 2 {
		fmt.Fprintln(d.errw, pluginUsage)
		return 2
	}
	op, name := args[0], args[1]
	switch op {
	case "connect":
		params := map[string]string{}
		for _, kv := range args[2:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				fmt.Fprintln(d.errw, pluginUsage)
				return 2
			}
			params[k] = v
		}
		conn.Send(ipc.TypePluginConnect, "c", ipc.PluginConnect{Plugin: name, Params: params})
		return d.waitConnect(conn, name)
	case "disconnect":
		conn.Send(ipc.TypePluginDisconnect, "d", ipc.PluginRef{Plugin: name})
		return d.printReply(conn, "d")
	case "option":
		if len(args) != 4 {
			fmt.Fprintln(d.errw, pluginUsage)
			return 2
		}
		conn.Send(ipc.TypePluginSetOption, "o", ipc.PluginSetOption{Plugin: name, Key: args[2], Value: args[3]})
		return d.printReply(conn, "o")
	case "tools":
		conn.Send(ipc.TypePluginTools, "t", ipc.PluginRef{Plugin: name})
		return d.printReply(conn, "t")
	default:
		fmt.Fprintln(d.errw, pluginUsage)
		return 2
	}
}

// waitConnect follows a plugin.connect like the app's plugin window: it
// prints the device code or authorization URL, opens the URL in the
// browser, and returns once the plugin is connected (0) or failed (1).
func (d *devctl) waitConnect(conn *ipc.Conn, plugin string) int {
	for {
		m, err := conn.Read()
		if err != nil {
			fmt.Fprintln(d.errw, err)
			return 1
		}
		switch m.Type {
		case ipc.TypeError:
			if m.ID == "c" {
				fmt.Fprintln(d.errw, string(m.Data))
				return 1
			}
		case ipc.TypePluginPrompt:
			p, _ := ipc.Decode[ipc.PluginPrompt](m)
			if p.Plugin != plugin {
				continue
			}
			if p.UserCode != "" {
				fmt.Fprintln(d.out, "code:", p.UserCode)
			}
			fmt.Fprintln(d.out, "open:", p.URL)
			d.open(p.URL)
		case ipc.TypePluginUpdated:
			p, _ := ipc.Decode[model.PluginState](m)
			if p.Plugin != plugin {
				continue
			}
			fmt.Fprintln(d.out, line(m))
			switch p.Status {
			case model.PluginConnected:
				return 0
			case model.PluginError, model.PluginExpired, model.PluginDisconnected:
				return 1
			}
		}
	}
}

func (d *devctl) printReply(conn *ipc.Conn, id string) int {
	for {
		m, err := conn.Read()
		if err != nil {
			fmt.Fprintln(d.errw, err)
			return 1
		}
		if m.ID == id {
			var v any
			json.Unmarshal(m.Data, &v)
			b, _ := json.MarshalIndent(v, "", "  ")
			fmt.Fprintln(d.out, m.Type)
			fmt.Fprintln(d.out, string(b))
			return 0
		}
	}
}

func line(m ipc.Message) string {
	return time.Now().Format("15:04:05.000") + " " + m.Type + " " + string(m.Data)
}
