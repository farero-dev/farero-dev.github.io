// Package gateway is the local MCP server agents connect to (F-05). It
// exposes one server, "farero", whose tools are the allowed tools of the
// connected plugins, and hands every call to a Caller that applies policy.
//
// Security (기능 명세서 5-5, 아키텍처 7-4): it listens on 127.0.0.1 only,
// rejects any request carrying an Origin header (agents are not browsers),
// and requires "Authorization: Bearer <secret>".
package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ConnHeader carries the per-connection id issued to the headersHelper.
const ConnHeader = "X-Farero-Conn"

// Call is one tools/call as seen by the gateway.
type Call struct {
	Name   string // exposed name, "<plugin>_<tool>"
	Args   json.RawMessage
	ConnID string
}

// Caller executes a gateway call (policy, approval, upstream, audit).
type Caller interface {
	CallTool(ctx context.Context, c Call) *mcp.CallToolResult
}

// Gateway owns the MCP server and its HTTP listener.
type Gateway struct {
	server *mcp.Server
	caller Caller
	secret string
	log    *slog.Logger

	mu    sync.Mutex
	tools map[string]string // exposed name -> JSON of the tool definition
	srv   *http.Server
	port  int
}

// New creates a gateway. secret is the bearer token agents must send.
func New(version, secret string, caller Caller, log *slog.Logger) *Gateway {
	if log == nil {
		log = slog.Default()
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "farero", Title: "farero gateway", Version: version}, &mcp.ServerOptions{
		Instructions: "Tools from services connected in farero. Some calls need the user's approval in the farero app; a refused call returns an error that says why.",
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
	})
	return &Gateway{server: s, caller: caller, secret: secret, log: log, tools: map[string]string{}}
}

// SetTools replaces the exposed tool set. Unchanged tools are left alone so
// clients only get a list_changed notification when something changed.
func (g *Gateway) SetTools(tools []*mcp.Tool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	want := map[string]*mcp.Tool{}
	for _, t := range tools {
		want[t.Name] = t
	}
	var remove []string
	for name := range g.tools {
		if _, ok := want[name]; !ok {
			remove = append(remove, name)
		}
	}
	if len(remove) > 0 {
		g.server.RemoveTools(remove...)
		for _, n := range remove {
			delete(g.tools, n)
		}
	}
	for name, t := range want {
		sig, _ := json.Marshal(t)
		if g.tools[name] == string(sig) {
			continue
		}
		if err := g.addTool(t); err != nil {
			g.log.Warn("skipping tool", "tool", name, "err", err)
			continue
		}
		g.tools[name] = string(sig)
	}
}

func (g *Gateway) addTool(t *mcp.Tool) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	name := t.Name
	g.server.AddTool(t, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		conn := ""
		if req.Extra != nil && req.Extra.Header != nil {
			conn = req.Extra.Header.Get(ConnHeader)
		}
		var args json.RawMessage
		if req.Params != nil {
			args = req.Params.Arguments
		}
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		return g.caller.CallTool(ctx, Call{Name: name, Args: args, ConnID: conn}), nil
	})
	return nil
}

// ToolNames returns the exposed tool names (for tests and status).
func (g *Gateway) ToolNames() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.tools))
	for n := range g.tools {
		out = append(out, n)
	}
	return out
}

// Handler returns the HTTP handler: auth and Origin checks around the MCP
// Streamable HTTP handler, served at /mcp.
func (g *Gateway) Handler() http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return g.server }, &mcp.StreamableHTTPOptions{
		Logger: g.log,
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser requests are not allowed", http.StatusForbidden)
			return
		}
		want := "Bearer " + g.secret
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="farero"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// Start listens on 127.0.0.1:port (0 picks a free port) and serves in the
// background. It returns the bound port.
func (g *Gateway) Start(port int) (int, error) {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return 0, err
	}
	srv := &http.Server{Handler: g.Handler(), ReadHeaderTimeout: 10 * time.Second}
	g.mu.Lock()
	g.srv = srv
	g.port = l.Addr().(*net.TCPAddr).Port
	bound := g.port
	g.mu.Unlock()
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			g.log.Error("gateway stopped", "err", err)
		}
	}()
	return bound, nil
}

// Port returns the bound port, or 0 when not listening.
func (g *Gateway) Port() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.port
}

// Shutdown stops the listener.
func (g *Gateway) Shutdown(ctx context.Context) error {
	g.mu.Lock()
	srv := g.srv
	g.srv = nil
	g.port = 0
	g.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// URL is the gateway endpoint for a port.
func URL(port int) string { return fmt.Sprintf("http://127.0.0.1:%d/mcp", port) }
