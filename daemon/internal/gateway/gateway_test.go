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
	"time"

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

// blockCaller waits until the call's context ends, like a call waiting for
// an approval card.
type blockCaller struct {
	started chan struct{}
	done    chan struct{} // closed when the call's context ended
	release chan struct{} // closed by the test so a failing test does not hang
}

func (b *blockCaller) CallTool(ctx context.Context, _ Call) *mcp.CallToolResult {
	close(b.started)
	select {
	case <-ctx.Done():
		close(b.done)
	case <-b.release:
	}
	return &mcp.CallToolResult{IsError: true}
}

// When the agent abandons a call (Esc while the approval card is open), the
// call's context must end so the card is withdrawn, whichever protocol the
// agent speaks: the old one cancels with notifications/cancelled, 2026-07-28
// just drops the request.
func TestAgentCancelEndsCall(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			b := &blockCaller{started: make(chan struct{}), done: make(chan struct{}), release: make(chan struct{})}
			g := New("test", "s3cret", b, nil)
			g.SetTools([]*mcp.Tool{tool("dev_write_sim")})
			port, err := g.Start(0)
			if err != nil {
				t.Fatal(err)
			}
			defer g.Shutdown(context.Background())
			defer close(b.release)
			cl := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil)
			cs, err := cl.Connect(context.Background(), &mcp.StreamableClientTransport{
				Endpoint:   URL(port),
				HTTPClient: &http.Client{Transport: headerRT{map[string]string{"Authorization": "Bearer s3cret"}}},
			}, &mcp.ClientSessionOptions{ProtocolVersion: version})
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			if got := cs.InitializeResult().ProtocolVersion; got != version {
				t.Fatalf("negotiated %s", got)
			}
			ctx, cancel := context.WithCancel(context.Background())
			go cs.CallTool(ctx, &mcp.CallToolParams{Name: "dev_write_sim", Arguments: map[string]any{}})
			select {
			case <-b.started:
			case <-time.After(5 * time.Second):
				t.Fatal("call did not arrive")
			}
			cancel()
			select {
			case <-b.done:
			case <-time.After(5 * time.Second):
				t.Fatal("the call kept waiting after the agent cancelled it")
			}
		})
	}
}

var protocols = []string{"2026-07-28", "2025-11-25"}

// Agents may speak 2026-07-28 or fall back to initialize (M0: Claude Code
// tries server/discover first). Both get the tools, the calls with their
// connection header, and list_changed notifications.
func TestBothProtocols(t *testing.T) {
	for _, version := range protocols {
		t.Run(version, func(t *testing.T) {
			g, rec, port := start(t)
			changed := make(chan struct{}, 1)
			cl := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, &mcp.ClientOptions{
				ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
					select {
					case changed <- struct{}{}:
					default:
					}
				},
			})
			cs, err := cl.Connect(context.Background(), &mcp.StreamableClientTransport{
				Endpoint:   URL(port),
				HTTPClient: &http.Client{Transport: headerRT{map[string]string{"Authorization": "Bearer s3cret", ConnHeader: "conn-" + version}}},
			}, &mcp.ClientSessionOptions{ProtocolVersion: version})
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			if got := cs.InitializeResult().ProtocolVersion; got != version {
				t.Fatalf("negotiated %s", got)
			}
			lt, err := cs.ListTools(context.Background(), nil)
			if err != nil || len(lt.Tools) != 2 {
				t.Fatalf("tools: %v %v", lt, err)
			}
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "dev_echo", Arguments: map[string]any{"text": "hi"}})
			if err != nil || res.IsError {
				t.Fatalf("call: %+v %v", res, err)
			}
			if len(rec.calls) != 1 || rec.calls[0].ConnID != "conn-"+version {
				t.Fatalf("calls: %+v", rec.calls)
			}
			// The 2026-07-28 client opens its subscriptions/listen stream
			// in the background; give it a moment before changing tools.
			deadline := time.Now().Add(5 * time.Second)
			for {
				g.SetTools([]*mcp.Tool{tool("dev_echo"), tool("dev_write_sim"), tool("dev_new_" + time.Now().Format("150405.000000"))})
				select {
				case <-changed:
					return
				case <-time.After(200 * time.Millisecond):
				}
				if time.Now().After(deadline) {
					t.Fatal("no tools/list_changed notification")
				}
			}
		})
	}
}
