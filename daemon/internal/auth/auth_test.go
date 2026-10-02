package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/secret"
	"golang.org/x/oauth2"
)

func TestLoopbackFlowPKCEAndResource(t *testing.T) {
	var gotVerifier, gotResource, gotCode string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		gotVerifier = r.Form.Get("code_verifier")
		gotResource = r.Form.Get("resource")
		gotCode = r.Form.Get("code")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at1", "token_type": "Bearer", "refresh_token": "rt1", "expires_in": 3600})
	}))
	defer tokenSrv.Close()
	cfg := &oauth2.Config{ClientID: "https://example.com/client.json", Endpoint: oauth2.Endpoint{AuthURL: "https://auth.example.com/authorize", TokenURL: tokenSrv.URL, AuthStyle: oauth2.AuthStyleInParams}, Scopes: []string{"full_access"}}

	var challenge string
	tok, err := LoopbackFlow(context.Background(), cfg, "/callback", func(p Prompt) {
		u, _ := url.Parse(p.URL)
		q := u.Query()
		challenge = q.Get("code_challenge")
		if q.Get("code_challenge_method") != "S256" || q.Get("resource") != "https://mcp.example.com/mcp" {
			t.Errorf("auth url: %s", p.URL)
		}
		redirect := q.Get("redirect_uri")
		if u2, _ := url.Parse(redirect); u2.Hostname() != "127.0.0.1" || u2.Path != "/callback" {
			t.Errorf("redirect: %s", redirect)
		}
		// The "browser" comes back.
		go http.Get(redirect + "?code=the-code&state=" + url.QueryEscape(q.Get("state")))
	}, Resource("https://mcp.example.com/mcp"))
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at1" || tok.RefreshToken != "rt1" {
		t.Fatalf("token: %+v", tok)
	}
	if gotCode != "the-code" || gotVerifier == "" || gotResource != "https://mcp.example.com/mcp" || challenge == "" {
		t.Fatalf("token request: code=%q verifier=%q resource=%q challenge=%q", gotCode, gotVerifier, gotResource, challenge)
	}
	if oauth2.S256ChallengeFromVerifier(gotVerifier) != challenge {
		t.Fatal("verifier does not match challenge")
	}
}

func TestLoopbackFlowRejectsWrongState(t *testing.T) {
	cfg := &oauth2.Config{ClientID: "c", Endpoint: oauth2.Endpoint{AuthURL: "https://a/authorize", TokenURL: "https://a/token"}}
	_, err := LoopbackFlow(context.Background(), cfg, "/callback", func(p Prompt) {
		u, _ := url.Parse(p.URL)
		go http.Get(u.Query().Get("redirect_uri") + "?code=x&state=forged")
	})
	if err == nil {
		t.Fatal("forged state accepted")
	}
}

func TestDeviceFlow(t *testing.T) {
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("scope") != "repo read:org" {
			t.Errorf("scope: %q", r.Form.Get("scope"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"device_code": "dc", "user_code": "ABCD-1234", "verification_uri": "https://github.com/login/device", "expires_in": 900, "interval": 1})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if polls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "gho_x", "token_type": "bearer", "scope": "repo,read:org"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cfg := &oauth2.Config{ClientID: "Iv1.x", Endpoint: oauth2.Endpoint{DeviceAuthURL: srv.URL + "/device", TokenURL: srv.URL + "/token"}, Scopes: []string{"repo", "read:org"}}
	var shown Prompt
	tok, err := DeviceFlow(context.Background(), cfg, func(p Prompt) { shown = p })
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "gho_x" || shown.UserCode != "ABCD-1234" || shown.URL != "https://github.com/login/device" {
		t.Fatalf("tok=%+v prompt=%+v", tok, shown)
	}
}

func TestRefreshRotatesAndSaves(t *testing.T) {
	var mu sync.Mutex
	var seen []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		mu.Lock()
		seen = append(seen, r.Form)
		n := len(seen)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("refresh_token") == "dead" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at" + string(rune('0'+n)), "refresh_token": "rt" + string(rune('0'+n)), "expires_in": 1})
	}))
	defer srv.Close()
	store := secret.NewMemoryStore()
	tk := Tokens{Store: store, Plugin: "resend"}
	cfg := &oauth2.Config{ClientID: "cid", Endpoint: oauth2.Endpoint{TokenURL: srv.URL}}
	old := &oauth2.Token{AccessToken: "at0", RefreshToken: "rt0", Expiry: time.Now().Add(-time.Minute)}
	ts := tk.Source(cfg, old, "https://mcp.resend.com/mcp", nil)

	tok, err := ts.Token()
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at1" {
		t.Fatalf("got %+v", tok)
	}
	if seen[0].Get("refresh_token") != "rt0" || seen[0].Get("resource") != "https://mcp.resend.com/mcp" || seen[0].Get("client_id") != "cid" {
		t.Fatalf("refresh request: %v", seen[0])
	}
	saved, _ := tk.Load()
	if saved.AccessToken != "at1" || saved.RefreshToken != "rt1" {
		t.Fatalf("saved: %+v", saved)
	}
	// Expire again: the rotated refresh token must be used.
	time.Sleep(1100 * time.Millisecond)
	if _, err := ts.Token(); err != nil {
		t.Fatal(err)
	}
	if seen[1].Get("refresh_token") != "rt1" {
		t.Fatalf("second refresh used %q", seen[1].Get("refresh_token"))
	}
}

func TestRefreshFailureCallsExpiredOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	calls := make(chan error, 5)
	tk := Tokens{Store: secret.NewMemoryStore(), Plugin: "gmail"}
	ts := tk.Source(&oauth2.Config{ClientID: "c", Endpoint: oauth2.Endpoint{TokenURL: srv.URL}},
		&oauth2.Token{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(-time.Hour)}, "", func(err error) { calls <- err })
	for i := 0; i < 3; i++ {
		if _, err := ts.Token(); err == nil {
			t.Fatal("expected error")
		}
	}
	<-calls
	select {
	case <-calls:
		t.Fatal("onExpired called twice")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRequireHTTPS(t *testing.T) {
	if _, err := FetchServerMeta(context.Background(), "http://insecure.example.com"); err == nil {
		t.Fatal("http issuer accepted")
	}
}
