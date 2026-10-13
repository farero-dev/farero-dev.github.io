package gmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The clamp in search: 0, negative and above 50 fall back to 10; 1 and 50
// pass through unchanged.
func TestSearchMaxResultsClamp(t *testing.T) {
	var got string
	mux := http.NewServeMux()
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("maxResults")
		json.NewEncoder(w).Encode(map[string]any{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p := &Plugin{client: srv.Client(), base: srv.URL}

	for _, tc := range []struct {
		in   int
		want string
	}{
		{0, "10"}, {-1, "10"}, {1, "1"}, {2, "2"}, {49, "49"}, {50, "50"}, {51, "10"}, {1000, "10"},
	} {
		got = ""
		if _, err := p.search(context.Background(), "x", tc.in); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("max %d: maxResults=%q, want %q", tc.in, got, tc.want)
		}
	}
}

// A request without query parameters must not end in a bare "?".
func TestGetOmitsEmptyQueryString(t *testing.T) {
	var uri string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uri = r.RequestURI
		w.Write([]byte(`{"labels":[]}`))
	}))
	t.Cleanup(srv.Close)
	p := &Plugin{client: srv.Client(), base: srv.URL}
	if _, err := p.labels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if uri != "/labels" {
		t.Fatalf("request URI %q, want /labels", uri)
	}
}

func messageWithBody(t *testing.T, data string) Message {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": "m", "payload": map[string]any{"mimeType": "text/plain", "body": map[string]string{"data": data}},
		})
	}))
	t.Cleanup(srv.Close)
	p := &Plugin{client: srv.Client(), base: srv.URL}
	m, err := p.message(context.Background(), "m")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Bodies are cut at maxBody bytes; exactly maxBody is kept whole.
func TestBodyTruncationBoundary(t *testing.T) {
	for _, tc := range []struct {
		n         int
		truncated bool
	}{
		{maxBody - 1, false}, {maxBody, false}, {maxBody + 1, true},
	} {
		m := messageWithBody(t, b64(strings.Repeat("a", tc.n)))
		want := tc.n
		if tc.truncated {
			want = maxBody
		}
		if len(m.Body) != want || m.Truncated != tc.truncated {
			t.Errorf("n=%d: len=%d truncated=%v, want len=%d truncated=%v", tc.n, len(m.Body), m.Truncated, want, tc.truncated)
		}
	}
}

// Gmail sends unpadded base64url, but padded input must keep working too.
func TestBodyBase64Variants(t *testing.T) {
	for n := 1; n <= 8; n++ {
		plain := strings.Repeat("x", n)
		for name, data := range map[string]string{
			"unpadded": b64(plain),
			"padded":   b64(plain) + strings.Repeat("=", (4-len(b64(plain))%4)%4),
		} {
			if got := messageWithBody(t, data).Body; got != plain {
				t.Errorf("%s n=%d (%q): body %q, want %q", name, n, data, got, plain)
			}
		}
	}
	// Partially padded input is completed rather than dropped.
	if got := messageWithBody(t, "YQ=").Body; got != "a" {
		t.Errorf("partial padding: body %q, want %q", got, "a")
	}
}

func TestPadBase64(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "YWJj": "YWJj", "YQ": "YQ==", "YWI": "YWI=", "YQ==": "YQ==", "YWJjZA": "YWJjZA==", "YWJjZGU": "YWJjZGU=",
		"Y": "Y===",
	} {
		if got := padBase64(in); got != want {
			t.Errorf("padBase64(%q) = %q, want %q", in, got, want)
		}
	}
}

// upstreamTruncate cuts to at most n bytes without splitting a rune, and
// never walks past either end of the string. Its caller only cuts bodies
// longer than n, so n > len(s) is not an input.
func TestUpstreamTruncate(t *testing.T) {
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{
		{"hello", 3, "hel"},
		{"hi", 2, "hi"}, // exactly the length: s[n] would be out of range
		{"hi", 0, ""},
		{"가나다", 3, "가"}, // 3 bytes per rune
		{"가나다", 4, "가"}, // backs off a cut inside 나
		{"가나다", 5, "가"},
		{"가나다", 6, "가나"},
		{"a가", 2, "a"},
		// Only continuation bytes: stops at 0 instead of indexing s[-1].
		{"\x80\x80\x80", 2, ""},
	} {
		if got := upstreamTruncate(tc.s, tc.n); got != tc.want {
			t.Errorf("upstreamTruncate(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
	}
}

// New talks to the Gmail API with the given client.
func TestNewUsesGmailAPI(t *testing.T) {
	c := &http.Client{}
	if p := New(c); p.client != c || p.base != apiBase {
		t.Fatalf("%+v", p)
	}
}

// Arguments that are not JSON become a tool error, not a failed call.
func TestCallToolBadInput(t *testing.T) {
	p := fakeAPI(t)
	res, err := p.CallTool(context.Background(), "search_messages", json.RawMessage(`{"query":`))
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "잘못된 입력") {
		t.Fatalf("%+v %v", res, err)
	}
}

// Email reads the account address from the profile.
func TestEmail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/profile", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"emailAddress": "me@example.com"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p := &Plugin{client: srv.Client(), base: srv.URL}
	if got, err := p.Email(context.Background()); err != nil || got != "me@example.com" {
		t.Fatalf("%q %v", got, err)
	}
}
