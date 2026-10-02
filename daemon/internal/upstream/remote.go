package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Remote is a plugin backed by a remote Streamable HTTP MCP server (GitHub,
// Railway, Resend). The connection is opened on first use and reopened after
// a failure; tool calls themselves are never retried.
type Remote struct {
	name     string
	endpoint string
	client   *http.Client
	version  string

	mu      sync.Mutex
	session *mcp.ClientSession
}

// NewRemote creates a remote MCP plugin. client must add authentication.
func NewRemote(name, endpoint string, client *http.Client, version string) *Remote {
	return &Remote{name: name, endpoint: endpoint, client: client, version: version}
}

// Name implements Plugin.
func (r *Remote) Name() string { return r.name }

func (r *Remote) conn(ctx context.Context) (*mcp.ClientSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session != nil {
		return r.session, nil
	}
	cl := mcp.NewClient(&mcp.Implementation{Name: "farero", Version: r.version}, nil)
	s, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: r.endpoint, HTTPClient: r.client}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", r.name, err)
	}
	r.session = s
	return s, nil
}

func (r *Remote) drop(s *mcp.ClientSession) {
	r.mu.Lock()
	if r.session == s {
		r.session = nil
	}
	r.mu.Unlock()
	s.Close()
}

// ListTools implements Plugin. Listing is read-only, so it is retried once
// on a fresh connection.
func (r *Remote) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	for attempt := 0; ; attempt++ {
		s, err := r.conn(ctx)
		if err != nil {
			return nil, err
		}
		var out []*mcp.Tool
		var lerr error
		for t, err := range s.Tools(ctx, nil) {
			if err != nil {
				lerr = err
				break
			}
			out = append(out, t)
		}
		if lerr == nil {
			return out, nil
		}
		r.drop(s)
		if attempt == 1 {
			return nil, lerr
		}
	}
}

// CallTool implements Plugin.
func (r *Remote) CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	s, err := r.conn(ctx)
	if err != nil {
		return nil, err
	}
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		// The connection may be broken; reconnect next time, but do not
		// resend this call.
		r.drop(s)
		return nil, err
	}
	return res, nil
}

// Close implements Plugin.
func (r *Remote) Close() error {
	r.mu.Lock()
	s := r.session
	r.session = nil
	r.mu.Unlock()
	if s != nil {
		return s.Close()
	}
	return nil
}
