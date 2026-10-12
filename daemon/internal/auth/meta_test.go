package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/secret"
	"golang.org/x/oauth2"
)

// withTLS serves h over HTTPS and points the package's HTTP client at it,
// so discovery runs against a real https URL.
func withTLS(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	old := httpClient
	httpClient = srv.Client()
	t.Cleanup(func() { httpClient = old })
	return srv
}

func serveJSON(v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
}

// RFC 8414 discovery: the well-known path goes before the issuer's path,
// the metadata needs a token endpoint, and every endpoint it names must be
// https.
func TestFetchServerMeta(t *testing.T) {
	mux := http.NewServeMux()
	srv := withTLS(t, mux)
	mux.HandleFunc("/.well-known/oauth-authorization-server/tenant", serveJSON(map[string]any{
		"issuer": srv.URL + "/tenant", "token_endpoint": srv.URL + "/token", "authorization_endpoint": srv.URL + "/authorize",
		"registration_endpoint": srv.URL + "/register", "device_authorization_endpoint": srv.URL + "/device",
	}))
	mux.HandleFunc("/.well-known/oauth-authorization-server/token-only", serveJSON(map[string]any{"token_endpoint": srv.URL + "/token"}))
	mux.HandleFunc("/.well-known/oauth-authorization-server/no-token", serveJSON(map[string]any{"authorization_endpoint": srv.URL + "/authorize"}))
	for i, field := range []string{"token_endpoint", "authorization_endpoint", "registration_endpoint", "device_authorization_endpoint"} {
		doc := map[string]any{"token_endpoint": srv.URL + "/token"}
		doc[field] = "http://plain.example.com/" + field
		mux.HandleFunc("/.well-known/oauth-authorization-server/plain"+string(rune('a'+i)), serveJSON(doc))
	}
	ctx := context.Background()

	m, err := FetchServerMeta(ctx, srv.URL+"/tenant/")
	if err != nil || m.TokenEndpoint != srv.URL+"/token" || m.DeviceAuthorizationEndpoint != srv.URL+"/device" {
		t.Fatalf("tenant: %+v %v", m, err)
	}
	// Endpoints a server does not have are left empty, not refused.
	if m, err := FetchServerMeta(ctx, srv.URL+"/token-only"); err != nil || m.RegistrationEndpoint != "" {
		t.Fatalf("token endpoint only: %+v %v", m, err)
	}
	if _, err := FetchServerMeta(ctx, srv.URL+"/no-token"); err == nil || !strings.Contains(err.Error(), "token_endpoint") {
		t.Errorf("no token endpoint: %v", err)
	}
	for i := 0; i < 4; i++ {
		if _, err := FetchServerMeta(ctx, srv.URL+"/plain"+string(rune('a'+i))); err == nil || !strings.Contains(err.Error(), "https") {
			t.Errorf("plain endpoint %d accepted: %v", i, err)
		}
	}
	if _, err := FetchServerMeta(ctx, srv.URL+"/missing"); err == nil {
		t.Error("404 accepted")
	}
}

// RFC 9728 discovery: the resource's own document first, the root document
// when that is missing, and an error without authorization servers.
func TestFetchResourceMeta(t *testing.T) {
	mux := http.NewServeMux()
	srv := withTLS(t, mux)
	mux.HandleFunc("/.well-known/oauth-protected-resource/direct", serveJSON(map[string]any{
		"resource": "direct", "authorization_servers": []string{srv.URL + "/a"}}))
	mux.HandleFunc("/.well-known/oauth-protected-resource/empty", serveJSON(map[string]any{"resource": "empty"}))
	mux.HandleFunc("/.well-known/oauth-protected-resource", serveJSON(map[string]any{
		"resource": "root", "authorization_servers": []string{srv.URL + "/root"}}))
	ctx := context.Background()

	if m, err := FetchResourceMeta(ctx, srv.URL+"/direct/"); err != nil || m.Resource != "direct" {
		t.Fatalf("direct: %+v %v", m, err)
	}
	if m, err := FetchResourceMeta(ctx, srv.URL+"/mcp"); err != nil || m.Resource != "root" || m.AuthorizationServers[0] != srv.URL+"/root" {
		t.Fatalf("fallback to the root document: %+v %v", m, err)
	}
	if _, err := FetchResourceMeta(ctx, srv.URL+"/empty"); err == nil || !strings.Contains(err.Error(), "authorization_servers") {
		t.Errorf("no authorization servers: %v", err)
	}

	bare := withTLS(t, http.NotFoundHandler())
	if _, err := FetchResourceMeta(ctx, bare.URL+"/mcp"); err == nil || !strings.Contains(err.Error(), "/.well-known/oauth-protected-resource/mcp") {
		t.Errorf("both missing: %v (want the first error)", err)
	}
}

// Metadata documents are read up to 1 MB.
func TestGetJSONLimit(t *testing.T) {
	mux := http.NewServeMux()
	srv := withTLS(t, mux)
	mux.HandleFunc("/small", serveJSON(map[string]any{"issuer": strings.Repeat("x", 1000)}))
	mux.HandleFunc("/big", serveJSON(map[string]any{"issuer": strings.Repeat("x", 2<<20)}))
	var m ServerMeta
	if err := getJSON(context.Background(), srv.URL+"/small", &m); err != nil || len(m.Issuer) != 1000 {
		t.Fatalf("small: %v %d", err, len(m.Issuer))
	}
	if err := getJSON(context.Background(), srv.URL+"/big", &m); err == nil {
		t.Fatal("a document over 1 MB was read")
	}
}

// The plugin HTTP client authenticates with the token and adds the static
// headers (GitHub's read-only header, for example).
func TestHTTPClientHeaders(t *testing.T) {
	got := make(chan http.Header, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got <- r.Header.Clone() }))
	defer srv.Close()
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "at1", TokenType: "Bearer"})

	if _, err := HTTPClient(ts, map[string]string{"X-MCP-Readonly": "true"}).Get(srv.URL); err != nil {
		t.Fatal(err)
	}
	if h := <-got; h.Get("Authorization") != "Bearer at1" || h.Get("X-MCP-Readonly") != "true" {
		t.Fatalf("headers: %v", h)
	}
	if _, err := HTTPClient(ts, nil).Get(srv.URL); err != nil {
		t.Fatal(err)
	}
	if h := <-got; h.Get("Authorization") != "Bearer at1" || h.Get("X-MCP-Readonly") != "" {
		t.Fatalf("headers without extras: %v", h)
	}
}

// The browser can come back with an error or without a code; both end the
// flow with an error instead of a token.
func TestLoopbackFlowCallbackErrors(t *testing.T) {
	cfg := &oauth2.Config{ClientID: "c", Endpoint: oauth2.Endpoint{AuthURL: "https://a/authorize", TokenURL: "https://a/token"}}
	for query, want := range map[string]string{
		"error=access_denied&error_description=nope": "authorization denied",
		"":          "no authorization code",
		"code=x":    "state mismatch",
		"code=&x=1": "no authorization code",
	} {
		_, err := LoopbackFlow(context.Background(), cfg, "/callback", func(p Prompt) {
			u, _ := url.Parse(p.URL)
			q := u.Query()
			back := q.Get("redirect_uri") + "?state=" + url.QueryEscape(q.Get("state"))
			if strings.HasPrefix(query, "code=x") {
				back = q.Get("redirect_uri") + "?" + query // no state at all
			} else if query != "" {
				back += "&" + query
			}
			go http.Get(back)
		})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", query, err, want)
		}
	}
}

// refreshOnce runs one refresh against a token endpoint that answers with
// reply, and returns the token (or error) and the form it was sent.
func refreshOnce(t *testing.T, cfg oauth2.Config, reply http.HandlerFunc) (*oauth2.Token, error, url.Values) {
	t.Helper()
	forms := make(chan url.Values, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		forms <- r.Form
		reply(w, r)
	}))
	t.Cleanup(srv.Close)
	cfg.Endpoint = oauth2.Endpoint{TokenURL: srv.URL}
	tk := Tokens{Store: secret.NewMemoryStore(), Plugin: "test"}
	ts := tk.Source(&cfg, &oauth2.Token{AccessToken: "old", RefreshToken: "rt0", Expiry: time.Now().Add(-time.Minute)}, "", nil)
	tok, err := ts.Token()
	return tok, err, <-forms
}

// A refresh sends client_secret only for clients that have one, accepts
// JSON and form-encoded answers, keeps the old refresh token when none comes
// back, and sets the expiry from expires_in.
func TestRefreshRequestAndAnswer(t *testing.T) {
	answer := func(ct, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", ct)
			io.WriteString(w, body)
		}
	}
	tok, err, form := refreshOnce(t, oauth2.Config{ClientID: "c", ClientSecret: "sec"},
		answer("application/json", `{"access_token":"at1","token_type":"Bearer","expires_in":3600}`))
	if err != nil || form.Get("client_secret") != "sec" || tok.AccessToken != "at1" || tok.RefreshToken != "rt0" {
		t.Fatalf("with secret: %+v %v %v", tok, err, form)
	}
	if d := time.Until(tok.Expiry); d < 3590*time.Second || d > 3600*time.Second {
		t.Fatalf("expiry in %v, want about an hour", d)
	}
	tok, err, form = refreshOnce(t, oauth2.Config{ClientID: "c"},
		answer("application/x-www-form-urlencoded", "access_token=at2&token_type=bearer&refresh_token=rt2&expires_in=0"))
	if _, sent := form["client_secret"]; err != nil || sent || tok.AccessToken != "at2" || tok.RefreshToken != "rt2" || !tok.Expiry.IsZero() {
		t.Fatalf("form answer: %+v %v %v", tok, err, form)
	}
}

// A refresh fails without an access token, and the error names the
// server's error code, or the HTTP status when there is none.
func TestRefreshErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   []string
	}{
		"no access token": {200, `{"token_type":"Bearer"}`, []string{"200"}},
		"error code":      {400, `{"error":"invalid_grant","error_description":"expired"}`, []string{"invalid_grant", "expired"}},
		"status only":     {500, ``, []string{"500"}},
	} {
		_, err, _ := refreshOnce(t, oauth2.Config{ClientID: "c"}, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		})
		for _, want := range tc.want {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %v, want %q", name, err, want)
			}
		}
	}
}
