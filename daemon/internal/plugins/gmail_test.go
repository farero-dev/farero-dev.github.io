package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/upstream/gmail"
	"golang.org/x/oauth2"
)

// toServer sends every request to a local server over plain http, so the
// Gmail plugin (whose API base is fixed) never reaches Google.
type toServer struct {
	host string
	base http.RoundTripper
}

func (s toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = "http", s.host
	return s.base.RoundTrip(r)
}

type fakeGmailAPI struct {
	mu     sync.Mutex
	auth   string
	path   string
	status int
}

// fakeGmail redirects the default transport (which auth.HTTPClient wraps) to
// a server that answers the profile call.
func fakeGmail(t *testing.T, email string) *fakeGmailAPI {
	t.Helper()
	f := &fakeGmailAPI{status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth, f.path = r.Header.Get("Authorization"), r.URL.Path
		f.mu.Unlock()
		w.WriteHeader(f.status)
		json.NewEncoder(w).Encode(map[string]string{"emailAddress": email})
	}))
	old := http.DefaultTransport
	http.DefaultTransport = toServer{host: strings.TrimPrefix(srv.URL, "http://"), base: old}
	t.Cleanup(func() { http.DefaultTransport = old; srv.Close() })
	return f
}

func gmailFixture(t *testing.T) (*Env, *Gmail) {
	t.Helper()
	env, _ := newEnv(t)
	return env, &Gmail{env: env}
}

func TestGmailConnect(t *testing.T) {
	env, g := gmailFixture(t)
	api := fakeGmail(t, "me@example.com")
	flow := stubLoopback(t, &oauth2.Token{AccessToken: "gm-access", RefreshToken: "gm-refresh", Expiry: time.Now().Add(time.Hour)}, nil)
	prompt, prompts := collectPrompt()

	err := g.Connect(context.Background(), map[string]string{"client_id": "cid", "client_secret": "csecret"}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	if flow.cfg.ClientID != "cid" || flow.cfg.ClientSecret != "csecret" ||
		flow.cfg.Endpoint != googleEndpoint || googleEndpoint.AuthURL != "https://accounts.google.com/o/oauth2/auth" || googleEndpoint.TokenURL != "https://oauth2.googleapis.com/token" ||
		len(flow.cfg.Scopes) != 1 || flow.cfg.Scopes[0] != gmail.Scope {
		t.Errorf("flow config = %+v", flow.cfg)
	}
	// Refresh tokens need offline access and a consent screen.
	if flow.path != "/callback" || flow.authURL.Get("access_type") != "offline" || flow.authURL.Get("prompt") != "consent" {
		t.Errorf("flow path %q url %v", flow.path, flow.authURL)
	}
	if len(*prompts) != 1 || (*prompts)[0].URL != "https://auth.example/authorize" {
		t.Errorf("prompts = %+v", *prompts)
	}
	// The client settings are kept as JSON next to the token.
	var c gmailClient
	if err := json.Unmarshal([]byte(secretValue(t, env.Secrets, "gmail", "client")), &c); err != nil || c != (gmailClient{ClientID: "cid", ClientSecret: "csecret"}) {
		t.Errorf("saved client = %+v %v", c, err)
	}
	if !strings.Contains(secretValue(t, env.Secrets, "gmail", "token"), `"refresh_token":"gm-refresh"`) {
		t.Error("token not stored")
	}
	if api.auth != "Bearer gm-access" || api.path != "/gmail/v1/users/me/profile" {
		t.Errorf("profile call: %q %q", api.auth, api.path)
	}
	if st := state(t, env, "gmail"); st.Status != model.PluginConnected || st.AccountLabel != "me@example.com" || st.ConnectedAt.IsZero() {
		t.Errorf("state = %+v", st)
	}
	p, ok := env.Core.Plugins().Get("gmail")
	if !ok || p.Name() != gmail.Name {
		t.Fatalf("plugin = %v %v", p, ok)
	}
}

func TestGmailConnectClientSettings(t *testing.T) {
	ctx := context.Background()
	noPrompt := func(ipc.PluginPrompt) {}
	tok := &oauth2.Token{AccessToken: "gm-access", Expiry: time.Now().Add(time.Hour)}

	t.Run("none given", func(t *testing.T) {
		env, g := gmailFixture(t)
		flow := stubLoopback(t, tok, nil)
		err := g.Connect(ctx, map[string]string{"client_secret": "orphan"}, noPrompt)
		if err == nil || !strings.HasPrefix(err.Error(), "gmail: ") || flow.calls != 0 {
			t.Errorf("err = %v, flow calls %d", err, flow.calls)
		}
		if hasSecret(env.Secrets, "gmail", "client") {
			t.Error("client saved")
		}
	})
	t.Run("saved ones are reused", func(t *testing.T) {
		env, g := gmailFixture(t)
		fakeGmail(t, "me@example.com")
		g.tokens().SetValue("client", `{"client_id":"saved-id","client_secret":"saved-secret"}`)
		flow := stubLoopback(t, tok, nil)
		if err := g.Connect(ctx, nil, noPrompt); err != nil {
			t.Fatal(err)
		}
		if flow.cfg.ClientID != "saved-id" || flow.cfg.ClientSecret != "saved-secret" {
			t.Errorf("flow config = %+v", flow.cfg)
		}
		if state(t, env, "gmail").Status != model.PluginConnected {
			t.Error("not connected")
		}
	})
	t.Run("given ones replace saved ones", func(t *testing.T) {
		env, g := gmailFixture(t)
		fakeGmail(t, "me@example.com")
		g.tokens().SetValue("client", `{"client_id":"saved-id","client_secret":"saved-secret"}`)
		flow := stubLoopback(t, tok, nil)
		// A new id without a secret does not inherit the old secret.
		if err := g.Connect(ctx, map[string]string{"client_id": "new-id"}, noPrompt); err != nil {
			t.Fatal(err)
		}
		if flow.cfg.ClientID != "new-id" || flow.cfg.ClientSecret != "" {
			t.Errorf("flow config = %+v", flow.cfg)
		}
		if got := secretValue(t, env.Secrets, "gmail", "client"); !strings.Contains(got, `"new-id"`) || strings.Contains(got, "saved") {
			t.Errorf("saved client = %s", got)
		}
	})
	t.Run("unreadable saved client", func(t *testing.T) {
		_, g := gmailFixture(t)
		g.tokens().SetValue("client", "garbage")
		stubLoopback(t, tok, nil)
		if err := g.Connect(ctx, nil, noPrompt); err == nil || !strings.Contains(err.Error(), "gmail: ") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestGmailConnectErrors(t *testing.T) {
	ctx := context.Background()
	noPrompt := func(ipc.PluginPrompt) {}
	params := map[string]string{"client_id": "cid"}
	t.Run("denied", func(t *testing.T) {
		env, g := gmailFixture(t)
		stubLoopback(t, nil, errors.New("authorization denied: access_denied "))
		err := g.Connect(ctx, params, noPrompt)
		if err == nil || err.Error() != "gmail: authorization denied: access_denied " {
			t.Errorf("err = %v", err)
		}
		if hasSecret(env.Secrets, "gmail", "client") || hasSecret(env.Secrets, "gmail", "token") || state(t, env, "gmail").Status == model.PluginConnected {
			t.Error("state left behind")
		}
	})
	t.Run("client save fails", func(t *testing.T) {
		env, g := gmailFixture(t)
		stubLoopback(t, &oauth2.Token{AccessToken: "t", Expiry: time.Now().Add(time.Hour)}, nil)
		env.Secrets.(*failSecrets).failSet = true
		if err := g.Connect(ctx, params, noPrompt); err == nil || !strings.Contains(err.Error(), "keychain locked") {
			t.Errorf("err = %v", err)
		}
		if _, ok := env.Core.Plugins().Get("gmail"); ok {
			t.Error("plugin registered")
		}
	})
	t.Run("profile lookup fails", func(t *testing.T) {
		env, g := gmailFixture(t)
		api := fakeGmail(t, "me@example.com")
		api.status = http.StatusForbidden
		stubLoopback(t, &oauth2.Token{AccessToken: "t", Expiry: time.Now().Add(time.Hour)}, nil)
		if err := g.Connect(ctx, params, noPrompt); err != nil {
			t.Fatal(err)
		}
		if st := state(t, env, "gmail"); st.Status != model.PluginConnected || st.AccountLabel != "" {
			t.Errorf("state = %+v", st)
		}
	})
}

func TestGmailRestoreAndDisconnect(t *testing.T) {
	env, g := gmailFixture(t)
	ctx := context.Background()
	fakeGmail(t, "me@example.com")
	if err := g.Restore(ctx); err == nil {
		t.Error("restored without client")
	}
	g.tokens().SetValue("client", `{"client_id":"cid"}`)
	if err := g.Restore(ctx); err == nil {
		t.Error("restored without token")
	}
	g.tokens().SetValue("client", "garbage")
	g.tokens().Save(&oauth2.Token{AccessToken: "gm-saved", Expiry: time.Now().Add(time.Hour)})
	if err := g.Restore(ctx); err == nil {
		t.Error("restored with an unreadable client")
	}
	g.tokens().SetValue("client", `{"client_id":"cid"}`)
	if err := g.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if st := state(t, env, "gmail"); st.Status != model.PluginConnected || st.AccountLabel != "" || st.ConnectedAt.IsZero() {
		t.Errorf("state = %+v", st)
	}
	if _, ok := env.Core.Plugins().Get("gmail"); !ok {
		t.Fatal("not registered")
	}
	// Disconnect keeps the client settings so reconnecting does not ask again.
	if err := g.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if hasSecret(env.Secrets, "gmail", "token") {
		t.Error("token survived Disconnect")
	}
	if !hasSecret(env.Secrets, "gmail", "client") {
		t.Error("Disconnect removed the client settings")
	}
}

func TestGmailConfig(t *testing.T) {
	_, g := gmailFixture(t)
	cfg := g.config(gmailClient{ClientID: "a", ClientSecret: "b"})
	if cfg.ClientID != "a" || cfg.ClientSecret != "b" || cfg.Scopes[0] != "https://www.googleapis.com/auth/gmail.readonly" || len(cfg.Scopes) != 1 {
		t.Errorf("config = %+v", cfg)
	}
}
