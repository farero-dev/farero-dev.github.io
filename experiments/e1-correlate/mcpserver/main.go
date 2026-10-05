// mcpserver is the M0 experiment 1/2 MCP server. It logs every HTTP request
// (arrival time, headers, raw JSON-RPC body) so the arrival order of
// PreToolUse hooks and MCP tool calls can be compared.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	logMu   sync.Mutex
	logFile *os.File
)

func record(kind string, v map[string]any) {
	v["src"] = kind
	v["ts_ns"] = time.Now().UnixNano()
	b, _ := json.Marshal(v)
	logMu.Lock()
	defer logMu.Unlock()
	logFile.Write(append(b, '\n'))
}

// capture records the status and the first bytes of a response.
type capture struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (c *capture) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

func (c *capture) Write(b []byte) (int, error) {
	if c.buf.Len() < 600 {
		c.buf.Write(b[:min(len(b), 600-c.buf.Len())])
	}
	return c.ResponseWriter.Write(b)
}

func (c *capture) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type echoArgs struct {
	Text  string         `json:"text"`
	Count int            `json:"count,omitempty"`
	Opts  map[string]any `json:"opts,omitempty"`
}

type slowArgs struct {
	Seconds int `json:"seconds"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:47811", "listen address")
	out := flag.String("log", "events.jsonl", "event log path")
	flag.Parse()

	var err error
	logFile, err = os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "farero-exp", Version: "0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "Echo the given text back. Optional count and opts object.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
		record("tool", map[string]any{"tool": "echo", "args": json.RawMessage(req.Params.Arguments)})
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + args.Text}}}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "slow",
		Description: "Wait the given number of seconds, then return 'done'.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args slowArgs) (*mcp.CallToolResult, any, error) {
		record("tool", map[string]any{"tool": "slow", "args": json.RawMessage(req.Params.Arguments)})
		select {
		case <-time.After(time.Duration(args.Seconds) * time.Second):
		case <-ctx.Done():
			record("tool", map[string]any{"tool": "slow", "cancelled": ctx.Err().Error()})
			return nil, nil, ctx.Err()
		}
		record("tool", map[string]any{"tool": "slow", "finished": true})
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil, nil
	})

	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		hdr := map[string]string{}
		for _, k := range []string{"Authorization", "X-Farero-Conn", "Mcp-Protocol-Version", "Mcp-Session-Id", "User-Agent", "Origin", "Accept"} {
			if v := r.Header.Get(k); v != "" {
				hdr[k] = v
			}
		}
		var rpc any
		if json.Unmarshal(body, &rpc) != nil {
			rpc = string(body)
		}
		record("http", map[string]any{"method": r.Method, "headers": hdr, "body": rpc})
		rw := &capture{ResponseWriter: w}
		h.ServeHTTP(rw, r)
		record("resp", map[string]any{"status": rw.status, "body": rw.buf.String()})
	})
	fmt.Fprintln(os.Stderr, "listening on", *addr)
	log.Fatal(http.ListenAndServe(*addr, wrapped))
}
