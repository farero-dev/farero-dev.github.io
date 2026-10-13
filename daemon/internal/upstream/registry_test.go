package upstream

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// closeCount is a plugin that counts how often it was closed.
type closeCount struct {
	name   string
	closed int
}

func (c *closeCount) Name() string { return c.name }
func (c *closeCount) ListTools(context.Context) ([]*mcp.Tool, error) {
	return nil, nil
}
func (c *closeCount) CallTool(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
	return nil, nil
}
func (c *closeCount) Close() error { c.closed++; return nil }

// Replacing a plugin closes the old connection (not the new one, nor the
// same one set again); removing it closes it.
func TestRegistryClosesReplacedAndRemoved(t *testing.T) {
	r := NewRegistry()
	a, b := &closeCount{name: "github"}, &closeCount{name: "github"}
	r.Set(a)
	r.Set(a)
	if a.closed != 0 {
		t.Fatalf("setting the same plugin again closed it %d times", a.closed)
	}
	r.Set(b)
	if a.closed != 1 || b.closed != 0 {
		t.Fatalf("replace: old closed %d, new closed %d", a.closed, b.closed)
	}
	if p, ok := r.Get("github"); !ok || p != b {
		t.Fatalf("get: %v %v", p, ok)
	}
	r.Remove("github")
	r.Remove("github") // nothing left to close
	if b.closed != 1 {
		t.Fatalf("remove: closed %d", b.closed)
	}
	if _, ok := r.Get("github"); ok {
		t.Fatal("still registered")
	}
}
