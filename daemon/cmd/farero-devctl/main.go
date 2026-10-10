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
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/paths"
	"github.com/farero-dev/farero/daemon/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: farero-devctl watch|answer|log|policy|agentcfg|plugin")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	conn, err := ipc.Dial(paths.Socket(), time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "farerod not reachable:", err)
		os.Exit(1)
	}
	defer conn.Close()
	conn.Send(ipc.TypeUIHello, "hello", ipc.UIHello{Version: "devctl"})
	snap, err := conn.Read()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	switch cmd {
	case "watch":
		fs := flag.NewFlagSet("watch", flag.ExitOnError)
		answer := fs.String("answer", "", "answer approvals automatically")
		fs.Parse(args)
		fmt.Println(line(snap))
		for {
			m, err := conn.Read()
			if err != nil {
				fmt.Fprintln(os.Stderr, "disconnected:", err)
				return
			}
			fmt.Println(line(m))
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
			fmt.Fprintln(os.Stderr, "usage: farero-devctl answer <approval id> allow|allow_session|deny")
			os.Exit(2)
		}
		conn.Send(ipc.TypeApprovalResponse, "r", ipc.ApprovalResponse{ApprovalID: args[0], Answer: args[1]})
		printReply(conn, "r")
	case "log":
		fs := flag.NewFlagSet("log", flag.ExitOnError)
		q := fs.String("q", "", "search text")
		n := fs.Int("n", 20, "rows")
		fs.Parse(args)
		conn.Send(ipc.TypeLogQuery, "q", store.CallFilter{Query: *q, Limit: *n})
		printReply(conn, "q")
	case "policy":
		if len(args) == 0 {
			conn.Send(ipc.TypePolicyGet, "p", nil)
			printReply(conn, "p")
			break
		}
		if args[0] != "set" || len(args) < 3 || len(args) > 4 {
			fmt.Fprintln(os.Stderr, "usage: farero-devctl policy [set <plugin> <tool> [auto|ask|block]]")
			os.Exit(2)
		}
		level := ""
		if len(args) == 4 {
			level = args[3]
		}
		conn.Send(ipc.TypePolicySet, "p", ipc.PolicySet{Plugin: args[1], Tool: args[2], Level: level})
		printReply(conn, "p")
	case "agentcfg":
		op := "status"
		if len(args) > 0 {
			op = args[0]
		}
		conn.Send("agentcfg."+op, "a", ipc.AgentRef{Agent: "claude"})
		printReply(conn, "a")
	case "plugin":
		pluginCmd(conn, args)
	default:
		fmt.Fprintln(os.Stderr, "unknown command", cmd)
		os.Exit(2)
	}
}

const pluginUsage = "usage: farero-devctl plugin [connect <plugin> [key=value…] | disconnect <plugin> | option <plugin> <key> <value> | tools <plugin>]"

func pluginCmd(conn *ipc.Conn, args []string) {
	if len(args) == 0 {
		conn.Send(ipc.TypePluginList, "l", nil)
		printReply(conn, "l")
		return
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, pluginUsage)
		os.Exit(2)
	}
	op, name := args[0], args[1]
	switch op {
	case "connect":
		params := map[string]string{}
		for _, kv := range args[2:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				fmt.Fprintln(os.Stderr, pluginUsage)
				os.Exit(2)
			}
			params[k] = v
		}
		conn.Send(ipc.TypePluginConnect, "c", ipc.PluginConnect{Plugin: name, Params: params})
		os.Exit(waitConnect(conn, name))
	case "disconnect":
		conn.Send(ipc.TypePluginDisconnect, "d", ipc.PluginRef{Plugin: name})
		printReply(conn, "d")
	case "option":
		if len(args) != 4 {
			fmt.Fprintln(os.Stderr, pluginUsage)
			os.Exit(2)
		}
		conn.Send(ipc.TypePluginSetOption, "o", ipc.PluginSetOption{Plugin: name, Key: args[2], Value: args[3]})
		printReply(conn, "o")
	case "tools":
		conn.Send(ipc.TypePluginTools, "t", ipc.PluginRef{Plugin: name})
		printReply(conn, "t")
	default:
		fmt.Fprintln(os.Stderr, pluginUsage)
		os.Exit(2)
	}
}

// waitConnect follows a plugin.connect like the app's plugin window: it
// prints the device code or authorization URL, opens the URL in the
// browser, and returns once the plugin is connected (0) or failed (1).
func waitConnect(conn *ipc.Conn, plugin string) int {
	for {
		m, err := conn.Read()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		switch m.Type {
		case ipc.TypeError:
			if m.ID == "c" {
				fmt.Fprintln(os.Stderr, string(m.Data))
				return 1
			}
		case ipc.TypePluginPrompt:
			p, _ := ipc.Decode[ipc.PluginPrompt](m)
			if p.Plugin != plugin {
				continue
			}
			if p.UserCode != "" {
				fmt.Println("code:", p.UserCode)
			}
			fmt.Println("open:", p.URL)
			exec.Command("open", p.URL).Run()
		case ipc.TypePluginUpdated:
			p, _ := ipc.Decode[model.PluginState](m)
			if p.Plugin != plugin {
				continue
			}
			fmt.Println(line(m))
			switch p.Status {
			case model.PluginConnected:
				return 0
			case model.PluginError, model.PluginExpired, model.PluginDisconnected:
				return 1
			}
		}
	}
}

func printReply(conn *ipc.Conn, id string) {
	for {
		m, err := conn.Read()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if m.ID == id {
			var v any
			json.Unmarshal(m.Data, &v)
			b, _ := json.MarshalIndent(v, "", "  ")
			fmt.Println(m.Type)
			fmt.Println(string(b))
			return
		}
	}
}

func line(m ipc.Message) string {
	return time.Now().Format("15:04:05.000") + " " + m.Type + " " + string(m.Data)
}
