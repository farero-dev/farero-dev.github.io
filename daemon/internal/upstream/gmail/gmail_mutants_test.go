package gmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
