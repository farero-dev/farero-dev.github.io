// farero-devctl is a development stand-in for the app: it connects to
// farerod as a UI client, prints every event, and can answer approval
// cards automatically. It is not shipped in the app bundle.
//
//	farero-devctl watch [-answer allow|allow_session|deny]
//	farero-devctl log [-q text] [-n 20]
//	farero-devctl policy
//	farero-devctl agentcfg status|plan|apply|remove
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/paths"
	"github.com/farero-dev/farero/daemon/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: farero-devctl watch|log|policy")
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
	case "log":
		fs := flag.NewFlagSet("log", flag.ExitOnError)
		q := fs.String("q", "", "search text")
		n := fs.Int("n", 20, "rows")
		fs.Parse(args)
		conn.Send(ipc.TypeLogQuery, "q", store.CallFilter{Query: *q, Limit: *n})
		printReply(conn, "q")
	case "policy":
		conn.Send(ipc.TypePolicyGet, "p", nil)
		printReply(conn, "p")
	case "agentcfg":
		op := "status"
		if len(args) > 0 {
			op = args[0]
		}
		conn.Send("agentcfg."+op, "a", ipc.AgentRef{Agent: "claude"})
		printReply(conn, "a")
	default:
		fmt.Fprintln(os.Stderr, "unknown command", cmd)
		os.Exit(2)
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
