package upstream

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestResultText(t *testing.T) {
	img := &mcp.ImageContent{Data: []byte("x"), MIMEType: "image/png"}
	for _, tc := range []struct {
		name string
		res  *mcp.CallToolResult
		want string
	}{
		{"nil", nil, ""},
		{"empty", &mcp.CallToolResult{}, ""},
		{"one text", TextResult("a"), "a"},
		{"two texts keep going", &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: "a"}, &mcp.TextContent{Text: "b"}, &mcp.TextContent{Text: "c"}}}, "a\nb\nc"},
		{"text then image", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "a"}, img}},
			`a` + "\n" + `{"type":"image","mimeType":"image/png","data":"eA=="}`},
		{"structured only", &mcp.CallToolResult{StructuredContent: map[string]any{"k": 1}}, `{"k":1}`},
		{"content wins over structured", &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "a"}}, StructuredContent: map[string]any{"k": 1}}, "a"},
	} {
		if got := ResultText(tc.res); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A read-only listing is retried once on a fresh connection, then given up.
func TestRemoteListToolsRetriesOnce(t *testing.T) {
	srvMCP := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	mcp.AddTool(srvMCP, &mcp.Tool{Name: "t", Description: "x"}, func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		return TextResult("ok"), nil, nil
	})
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srvMCP }, nil)
	var lists atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"tools/list"`)) {
			lists.Add(1)
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		inner.ServeHTTP(w, r)
	})
	front := httptest.NewServer(h)
	t.Cleanup(front.Close)

	r := NewRemote("railway", front.URL, &http.Client{Transport: bearer{}}, "test")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := r.ListTools(ctx)
	r.Close()
	if err == nil {
		t.Fatal("expected an error")
	}
	if ctx.Err() != nil {
		t.Fatal("ListTools kept retrying until the deadline")
	}
	if n := lists.Load(); n != 2 {
		t.Fatalf("tools/list attempts: %d, want 2", n)
	}
}
