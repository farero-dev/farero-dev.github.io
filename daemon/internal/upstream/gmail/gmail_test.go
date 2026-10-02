package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func fakeAPI(t *testing.T) *Plugin {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "from:alice" {
			t.Errorf("query: %s", r.URL.RawQuery)
		}
		json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{{"id": "m1"}}})
	})
	mux.HandleFunc("/messages/m1", func(w http.ResponseWriter, r *http.Request) {
		msg := map[string]any{
			"id": "m1", "threadId": "t1", "labelIds": []string{"INBOX"}, "snippet": "Hi &amp; welcome",
			"payload": map[string]any{
				"mimeType": "multipart/alternative",
				"headers": []map[string]string{
					{"name": "From", "value": "Alice <alice@example.com>"},
					{"name": "Subject", "value": "배포 완료"},
					{"name": "Date", "value": "Thu, 1 Oct 2026 10:00:00 +0900"},
				},
				"parts": []map[string]any{
					{"mimeType": "text/html", "body": map[string]string{"data": b64("<p>html only</p>")}},
					{"mimeType": "text/plain", "body": map[string]string{"data": b64("본문입니다\nline 2")}},
				},
			},
		}
		json.NewEncoder(w).Encode(msg)
	})
	mux.HandleFunc("/threads/t2", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]any{{
			"id": "m2", "threadId": "t2",
			"payload": map[string]any{"mimeType": "text/html", "body": map[string]string{"data": b64("<html><style>x{}</style><body>Hello<br>World &lt;3</body></html>")}},
		}}})
	})
	mux.HandleFunc("/labels", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"labels": []map[string]string{{"id": "INBOX", "name": "INBOX", "type": "system"}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Plugin{client: srv.Client(), base: srv.URL}
}

func call(t *testing.T, p *Plugin, tool, args string) string {
	t.Helper()
	res, err := p.CallTool(context.Background(), tool, json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func TestSearchAndRead(t *testing.T) {
	p := fakeAPI(t)
	out := call(t, p, "search_messages", `{"query":"from:alice"}`)
	if !strings.Contains(out, `"subject": "배포 완료"`) || !strings.Contains(out, "Hi & welcome") {
		t.Fatalf("search: %s", out)
	}
	out = call(t, p, "get_message", `{"id":"m1"}`)
	if !strings.Contains(out, `본문입니다\nline 2`) {
		t.Fatalf("message should prefer text/plain: %s", out)
	}
	out = call(t, p, "get_thread", `{"id":"t2"}`)
	if !strings.Contains(out, `Hello\nWorld <3`) || strings.Contains(out, "x{}") {
		t.Fatalf("html fallback: %s", out)
	}
	out = call(t, p, "list_labels", `{}`)
	if !strings.Contains(out, "INBOX") {
		t.Fatalf("labels: %s", out)
	}
}

func TestToolsMatchPolicyNames(t *testing.T) {
	want := map[string]bool{"search_messages": true, "get_message": true, "get_thread": true, "list_labels": true}
	for _, tl := range tools {
		if !want[tl.Name] {
			t.Errorf("tool %s is not in policy/default.json", tl.Name)
		}
	}
}
