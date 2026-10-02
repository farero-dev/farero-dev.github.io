// hooklog is the M0 experiment hook command. It appends the hook stdin JSON
// plus a nanosecond timestamp, pid/ppid and tty to the event log.
//
//	hooklog -log events.jsonl                 # record and exit 0
//	hooklog -log events.jsonl -sleep 620 -allow   # PermissionRequest: wait, then allow
//	hooklog -headers -log events.jsonl        # headersHelper mode
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func main() {
	out := flag.String("log", "events.jsonl", "event log path")
	headers := flag.Bool("headers", false, "act as headersHelper")
	sleep := flag.Int("sleep", 0, "seconds to wait before answering")
	allow := flag.Bool("allow", false, "answer PermissionRequest with allow")
	flag.Parse()

	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(0)
	}
	defer f.Close()

	rec := map[string]any{
		"ts_ns": time.Now().UnixNano(),
		"pid":   os.Getpid(),
		"ppid":  os.Getppid(),
		"tty":   ttyOf(os.Getppid()),
	}
	if *headers {
		b := make([]byte, 8)
		rand.Read(b)
		conn := hex.EncodeToString(b)
		env := map[string]string{}
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "CLAUDE") || strings.HasPrefix(kv, "MCP") {
				k, v, _ := strings.Cut(kv, "=")
				env[k] = v
			}
		}
		rec["src"] = "headers"
		rec["conn"] = conn
		rec["env"] = env
		writeRec(f, rec)
		json.NewEncoder(os.Stdout).Encode(map[string]string{
			"Authorization": "Bearer exp-secret",
			"X-Farero-Conn": conn,
		})
		return
	}

	in, _ := io.ReadAll(os.Stdin)
	rec["src"] = "hook"
	rec["input"] = json.RawMessage(in)
	writeRec(f, rec)

	if *sleep > 0 {
		time.Sleep(time.Duration(*sleep) * time.Second)
		writeRec(f, map[string]any{"src": "hook", "ts_ns": time.Now().UnixNano(), "slept": *sleep})
	}
	if *allow {
		fmt.Println(`{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`)
	}
}

func writeRec(f *os.File, v map[string]any) {
	b, _ := json.Marshal(v)
	f.Write(append(b, '\n'))
}

// ttyOf walks up from pid looking for the first ancestor with a controlling tty.
func ttyOf(pid int) string {
	for i := 0; i < 6 && pid > 1; i++ {
		out, err := exec.Command("ps", "-o", "tty=,ppid=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return ""
		}
		fields := strings.Fields(string(out))
		if len(fields) < 2 {
			return ""
		}
		if fields[0] != "??" && fields[0] != "" {
			return fields[0]
		}
		pid, _ = strconv.Atoi(fields[1])
	}
	return ""
}
