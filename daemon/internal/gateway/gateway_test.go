package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type recCaller struct {
	mu    sync.Mutex
	calls []Call
}

func (r *recCaller) CallTool(_ context.Context, c Call) *mcp.CallToolResult {
	r.mu.Lock()
	r.calls = append(r.calls, c)
	r.mu.Unlock()
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok " + c.Name}}}
}

type headerRT struct{ h map[string]string }

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func tool(name string) *mcp.Tool {
	return &mcp.Tool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func start(t *testing.T) (*Gateway, *recCaller, int) {
	t.Helper()
	c := &recCaller{}
	g := New("test", "s3cret", c, nil)
	g.SetTools([]*mcp.Tool{tool("dev_echo"), tool("dev_write_sim")})
	port, err := g.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Shutdown(context.Background()) })
	return g, c, port
}

func connect(t *testing.T, port int, headers map[string]string) (*mcp.ClientSession, error) {
	t.Helper()
	cl := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	return cl.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   URL(port),
		HTTPClient: &http.Client{Transport: headerRT{headers}},
	}, nil)
}

func TestListAndCallWithConnHeader(t *testing.T) {
	_, rec, port := start(t)
	cs, err := connect(t, port, map[string]string{"Authorization": "Bearer s3cret", ConnHeader: "conn-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	lt, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range lt.Tools {
		names = append(names, x.Name)
	}
	sort.Strings(names)
	if !slices.Equal(names, []string{"dev_echo", "dev_write_sim"}) {
		t.Fatalf("tools: %v", names)
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "dev_echo", Arguments: map[string]any{"text": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content[0].(*mcp.TextContent).Text != "ok dev_echo" {
		t.Fatalf("result: %+v", res)
	}
	if len(rec.calls) != 1 || rec.calls[0].ConnID != "conn-1" || string(rec.calls[0].Args) != `{"text":"hi"}` {
		t.Fatalf("calls: %+v", rec.calls)
	}
}

func TestAuthAndOrigin(t *testing.T) {
	_, _, port := start(t)
	if _, err := connect(t, port, map[string]string{"Authorization": "Bearer wrong"}); err == nil {
		t.Fatal("wrong secret accepted")
	}
	req, _ := http.NewRequest("POST", URL(port), strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer s3cret")
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Origin request: status %d", resp.StatusCode)
	}
}

func TestSetToolsRemovesAndSkipsBadSchema(t *testing.T) {
	g, _, _ := start(t)
	bad := &mcp.Tool{Name: "dev_bad", InputSchema: json.RawMessage(`{"type":"string"}`)}
	g.SetTools([]*mcp.Tool{tool("dev_echo"), bad})
	names := g.ToolNames()
	if !slices.Equal(names, []string{"dev_echo"}) {
		t.Fatalf("tools: %v", names)
	}
}
