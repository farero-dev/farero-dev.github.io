package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoIn struct {
	Text string `json:"text"`
}

func fakeMCP(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "redeploy", Description: "x"}, func(ctx context.Context, req *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
		calls.Add(1)
		return TextResult("redeployed " + in.Text), nil, nil
	})
	var auth atomic.Value
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type bearer struct{}

func (bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer tok")
	return http.DefaultTransport.RoundTrip(r)
}

func TestRemoteListAndCall(t *testing.T) {
	var calls atomic.Int32
	srv := fakeMCP(t, &calls)
	r := NewRemote("railway", srv.URL, &http.Client{Transport: bearer{}}, "test")
	defer r.Close()
	tools, err := r.ListTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "redeploy" {
		t.Fatalf("tools: %v %v", tools, err)
	}
	res, err := r.CallTool(context.Background(), "redeploy", json.RawMessage(`{"text":"api"}`))
	if err != nil || ResultText(res) != "redeployed api" {
		t.Fatalf("call: %v %v", res, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls: %d", calls.Load())
	}
}

func TestRemoteUnauthorized(t *testing.T) {
	var calls atomic.Int32
	srv := fakeMCP(t, &calls)
	r := NewRemote("railway", srv.URL, http.DefaultClient, "test")
	if _, err := r.ListTools(context.Background()); err == nil {
		t.Fatal("expected an error without a token")
	}
}
