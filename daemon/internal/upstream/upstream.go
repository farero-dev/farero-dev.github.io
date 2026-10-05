// Package upstream connects farerod to the services behind plugin tools:
// remote MCP servers (GitHub, Railway, Resend) and farero's own Gmail tools.
package upstream

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Plugin is one connected service.
type Plugin interface {
	// Name is the plugin id used in tool names and the policy table.
	Name() string
	// ListTools returns the upstream tools with their original names.
	ListTools(ctx context.Context) ([]*mcp.Tool, error)
	// CallTool calls an upstream tool. It must not retry: a write could run
	// twice (기능 명세서 5-4).
	CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error)
	// Close releases connections.
	Close() error
}

// Registry holds the connected plugins.
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{plugins: map[string]Plugin{}} }

// Set adds or replaces a plugin, closing the one it replaces.
func (r *Registry) Set(p Plugin) {
	r.mu.Lock()
	old := r.plugins[p.Name()]
	r.plugins[p.Name()] = p
	r.mu.Unlock()
	if old != nil && old != p {
		old.Close()
	}
}

// Remove disconnects a plugin.
func (r *Registry) Remove(name string) {
	r.mu.Lock()
	p := r.plugins[name]
	delete(r.plugins, name)
	r.mu.Unlock()
	if p != nil {
		p.Close()
	}
}

// Get returns a plugin.
func (r *Registry) Get(name string) (Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.plugins[name]
	return p, ok
}

// All returns the plugins sorted by name.
func (r *Registry) All() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Close closes every plugin.
func (r *Registry) Close() {
	for _, p := range r.All() {
		r.Remove(p.Name())
	}
}

// ResultText flattens a tool result for the audit log: text content as is,
// other content as JSON, then structured content.
func ResultText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range res.Content {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
			continue
		}
		j, _ := json.Marshal(c)
		b.Write(j)
	}
	if res.StructuredContent != nil && len(res.Content) == 0 {
		j, _ := json.Marshal(res.StructuredContent)
		b.Write(j)
	}
	return b.String()
}

// ErrorResult builds a tool execution error the model can read (5-4).
func ErrorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// TextResult builds a plain text result.
func TextResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
