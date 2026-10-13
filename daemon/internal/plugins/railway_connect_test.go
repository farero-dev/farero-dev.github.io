package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/auth"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"golang.org/x/oauth2"
)

// registrationServer answers dynamic client registration and records the
// request.
type registrationServer struct {
	srv         *httptest.Server
	calls       int
	contentType string
	body        map[string]any
}

func newRegistrationServer(t *testing.T) *registrationServer {
	t.Helper()
	rs := &registrationServer{}
	rs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.calls++
		rs.contentType = r.Header.Get("Content-Type")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		json.NewDecoder(r.Body).Decode(&rs.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"client_id":"rlwy_oaci_new"}`))
	}))
	t.Cleanup(rs.srv.Close)
	return rs
}

func railwayFixture(t *testing.T) (*Env, *Railway, *fakeMCP) {
	t.Helper()
	env, _ := newEnv(t)
	mcpSrv := newFakeMCP(t)
	return env, &Railway{env: env}, mcpSrv
}

func railwayMetas(t *testing.T, mcpURL, regURL string) (resource, issuer *string) {
	t.Helper()
	return stubMeta(t,
		&auth.ResourceMeta{Resource: mcpURL, AuthorizationServers: []string{"https://backboard.example/first", "https://other.example"}}, nil,
		&auth.ServerMeta{AuthorizationEndpoint: "https://backboard.example/oauth/auth", TokenEndpoint: "https://backboard.example/oauth/token", RegistrationEndpoint: regURL}, nil)
}

func TestRailwayConnect(t *testing.T) {
	env, r, mcpSrv := railwayFixture(t)
	reg := newRegistrationServer(t)
	resource, issuer := railwayMetas(t, mcpSrv.srv.URL, reg.srv.URL)
	flow := stubLoopback(t, &oauth2.Token{AccessToken: "rw-access", RefreshToken: "rw-refresh"}, nil)
	prompt, prompts := collectPrompt()

	if err := r.Connect(context.Background(), nil, prompt); err != nil {
		t.Fatal(err)
	}
	if *resource != railwayMCP || *issuer != "https://backboard.example/first" {
		t.Errorf("discovery asked %q and %q", *resource, *issuer)
	}
	// The registration asks for a loopback public client.
	if reg.calls != 1 || reg.contentType != "application/json" || reg.body["client_name"] != "farero" ||
		reg.body["client_uri"] != "https://github.com/farero-dev/farero-dev.github.io" ||
		reg.body["scope"] != "openid profile email offline_access workspace:member" {
		t.Errorf("registration = %d %q %v", reg.calls, reg.contentType, reg.body)
	}
	// The flow gets the registered client, the discovered endpoints, PKCE
	// scopes and the resource indicator.
	if flow.cfg.ClientID != "rlwy_oaci_new" || flow.cfg.Endpoint.AuthURL != "https://backboard.example/oauth/auth" ||
		flow.cfg.Endpoint.TokenURL != "https://backboard.example/oauth/token" || flow.cfg.Endpoint.AuthStyle != oauth2.AuthStyleInParams {
		t.Errorf("flow config = %+v", flow.cfg)
	}
	if flow.path != "/callback" || flow.authURL.Get("resource") != mcpSrv.srv.URL ||
		flow.authURL.Get("scope") != "openid profile email offline_access workspace:member" {
		t.Errorf("flow path %q url %v", flow.path, flow.authURL)
	}
	if len(*prompts) != 1 || (*prompts)[0].URL != "https://auth.example/authorize" || !(*prompts)[0].ExpiresAt.Equal(time.Unix(1000, 0)) || (*prompts)[0].UserCode != "" {
		t.Errorf("prompts = %+v", *prompts)
	}
	// Token and setup are stored; the setup is what a restart reuses.
	tok, err := r.tokens().Load()
	if err != nil || tok.AccessToken != "rw-access" || tok.RefreshToken != "rw-refresh" {
		t.Fatalf("token = %+v %v", tok, err)
	}
	var saved railwaySetup
	if err := json.Unmarshal([]byte(secretValue(t, env.Secrets, "railway", "setup")), &saved); err != nil ||
		saved != (railwaySetup{Resource: mcpSrv.srv.URL, ClientID: "rlwy_oaci_new", AuthURL: "https://backboard.example/oauth/auth", TokenURL: "https://backboard.example/oauth/token"}) {
		t.Errorf("saved setup = %+v %v", saved, err)
	}
	// The label is the first line of whoami without Markdown.
	st := state(t, env, "railway")
	if st.Status != model.PluginConnected || st.AccountLabel != "Seong (@seongj-un)" || st.ConnectedAt.IsZero() {
		t.Errorf("state = %+v", st)
	}
	listTools(t, env, "railway")
	if got := mcpSrv.header("Authorization"); got != "Bearer rw-access" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestRailwayConnectLabelFallbacks(t *testing.T) {
	cases := []struct {
		name   string
		whoami string
		isErr  bool
		want   string
	}{
		{"tool error", "", true, ""},
		{"long account", strings.Repeat("가", 80), false, strings.Repeat("가", 60)},
		{"exactly the limit", strings.Repeat("a", 60), false, strings.Repeat("a", 60)},
	}
	for _, tc := range cases {
		env, r, mcpSrv := railwayFixture(t)
		mcpSrv.whoami, mcpSrv.whoamiErr = tc.whoami, tc.isErr
		railwayMetas(t, mcpSrv.srv.URL, newRegistrationServer(t).srv.URL)
		stubLoopback(t, &oauth2.Token{AccessToken: "t"}, nil)
		if err := r.Connect(context.Background(), nil, func(ipc.PluginPrompt) {}); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := state(t, env, "railway"); got.AccountLabel != tc.want || got.Status != model.PluginConnected {
			t.Errorf("%s: state = %+v", tc.name, got)
		}
	}
	// whoami unreachable: still connected, no label.
	env, r, mcpSrv := railwayFixture(t)
	railwayMetas(t, mcpSrv.srv.URL, newRegistrationServer(t).srv.URL)
	stubLoopback(t, &oauth2.Token{AccessToken: "t"}, nil)
	mcpSrv.srv.Close()
	if err := r.Connect(context.Background(), nil, func(ipc.PluginPrompt) {}); err != nil {
		t.Fatal(err)
	}
	if got := state(t, env, "railway"); got.AccountLabel != "" || got.Status != model.PluginConnected {
		t.Errorf("unreachable: state = %+v", got)
	}
}

func TestRailwayConnectErrors(t *testing.T) {
	ctx := context.Background()
	noPrompt := func(ipc.PluginPrompt) {}
	t.Run("denied", func(t *testing.T) {
		env, r, mcpSrv := railwayFixture(t)
		railwayMetas(t, mcpSrv.srv.URL, newRegistrationServer(t).srv.URL)
		stubLoopback(t, nil, errors.New("authorization denied: access_denied user said no"))
		err := r.Connect(ctx, nil, noPrompt)
		if err == nil || err.Error() != "railway: authorization denied: access_denied user said no" {
			t.Errorf("err = %v", err)
		}
		if hasSecret(env.Secrets, "railway", "token") || state(t, env, "railway").Status == model.PluginConnected {
			t.Error("denied connect left a token or a connected state")
		}
		if _, ok := env.Core.Plugins().Get("railway"); ok {
			t.Error("plugin registered")
		}
	})
	t.Run("discovery fails", func(t *testing.T) {
		_, r, _ := railwayFixture(t)
		stubMeta(t, nil, errors.New("no resource metadata"), nil, nil)
		flow := stubLoopback(t, nil, nil)
		err := r.Connect(ctx, nil, noPrompt)
		if err == nil || err.Error() != "railway: no resource metadata" || flow.calls != 0 {
			t.Errorf("err = %v, flow calls %d", err, flow.calls)
		}
	})
	t.Run("server metadata fails", func(t *testing.T) {
		_, r, _ := railwayFixture(t)
		stubMeta(t, &auth.ResourceMeta{Resource: "r", AuthorizationServers: []string{"i"}}, nil, nil, errors.New("no server metadata"))
		if err := r.Connect(ctx, nil, noPrompt); err == nil || err.Error() != "railway: no server metadata" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("unsupported server", func(t *testing.T) {
		for name, sm := range map[string]*auth.ServerMeta{
			"no authorization endpoint": {RegistrationEndpoint: "https://x/reg", TokenEndpoint: "https://x/t"},
			"no registration endpoint":  {AuthorizationEndpoint: "https://x/a", TokenEndpoint: "https://x/t"},
		} {
			env, r, _ := railwayFixture(t)
			stubMeta(t, &auth.ResourceMeta{Resource: "r", AuthorizationServers: []string{"i"}}, nil, sm, nil)
			err := r.Connect(ctx, nil, noPrompt)
			if err == nil || !strings.Contains(err.Error(), "DCR") || !strings.HasPrefix(err.Error(), "railway: ") {
				t.Errorf("%s: err = %v", name, err)
			}
			if hasSecret(env.Secrets, "railway", "setup") {
				t.Errorf("%s: setup saved", name)
			}
		}
	})
	t.Run("registration fails", func(t *testing.T) {
		env, r, _ := railwayFixture(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "  nope\n", http.StatusForbidden)
		}))
		defer srv.Close()
		railwayMetas(t, "r", srv.URL)
		err := r.Connect(ctx, nil, noPrompt)
		if err == nil || err.Error() != "railway: client registration: 403 Forbidden nope" {
			t.Errorf("err = %v", err)
		}
		if hasSecret(env.Secrets, "railway", "setup") {
			t.Error("setup saved")
		}
	})
	t.Run("save fails", func(t *testing.T) {
		env, r, mcpSrv := railwayFixture(t)
		railwayMetas(t, mcpSrv.srv.URL, newRegistrationServer(t).srv.URL)
		stubLoopback(t, &oauth2.Token{AccessToken: "t"}, nil)
		env.Secrets.(*failSecrets).failSet = true
		if err := r.Connect(ctx, nil, noPrompt); err == nil || !strings.Contains(err.Error(), "keychain locked") {
			t.Errorf("err = %v", err)
		}
		if _, ok := env.Core.Plugins().Get("railway"); ok {
			t.Error("plugin registered")
		}
	})
}

func TestRailwaySetupReuse(t *testing.T) {
	_, r, mcpSrv := railwayFixture(t)
	ctx := context.Background()
	noMeta(t)
	r.tokens().SetValue("setup", `{"resource":"`+mcpSrv.srv.URL+`","client_id":"saved","auth_url":"https://a","token_url":"https://t"}`)
	stubLoopback(t, &oauth2.Token{AccessToken: "t"}, nil)
	// A saved setup means no discovery and no second registration.
	if err := r.Connect(ctx, nil, func(ipc.PluginPrompt) {}); err != nil {
		t.Fatal(err)
	}
	s, err := r.setup(ctx)
	if err != nil || s.ClientID != "saved" || s.Resource != mcpSrv.srv.URL || s.AuthURL != "https://a" || s.TokenURL != "https://t" {
		t.Errorf("setup = %+v %v", s, err)
	}
}

func TestRailwayRestoreAndDisconnect(t *testing.T) {
	env, r, mcpSrv := railwayFixture(t)
	ctx := context.Background()
	// Without a saved setup the restore discovers again, which fails offline.
	stubMeta(t, nil, errors.New("offline"), nil, nil)
	if err := r.Restore(ctx); err == nil || err.Error() != "offline" {
		t.Errorf("restored without setup: %v", err)
	}
	noMeta(t)
	r.tokens().SetValue("setup", `{"resource":"`+mcpSrv.srv.URL+`","client_id":"saved","auth_url":"https://a","token_url":"https://t"}`)
	if err := r.Restore(ctx); err == nil {
		t.Error("restored without token")
	}
	r.tokens().Save(&oauth2.Token{AccessToken: "rw-saved", Expiry: time.Now().Add(time.Hour)})
	if err := r.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if st := state(t, env, "railway"); st.Status != model.PluginConnected || st.AccountLabel != "" || st.ConnectedAt.IsZero() {
		t.Errorf("state = %+v", st)
	}
	listTools(t, env, "railway")
	if got := mcpSrv.header("Authorization"); got != "Bearer rw-saved" {
		t.Errorf("Authorization = %q", got)
	}

	// Disconnect drops the token but keeps the client registration.
	if err := r.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if hasSecret(env.Secrets, "railway", "token") {
		t.Error("token survived Disconnect")
	}
	if !hasSecret(env.Secrets, "railway", "setup") {
		t.Error("Disconnect removed the client registration")
	}
}

// The resource indicator goes on refresh requests too (MCP authorization
// spec), with the registered client id.
func TestRailwayRefreshSendsResource(t *testing.T) {
	env, r, mcpSrv := railwayFixture(t)
	noMeta(t)
	ts := newTokenServer(t)
	r.tokens().SetValue("setup", `{"resource":"`+mcpSrv.srv.URL+`","client_id":"saved","auth_url":"https://a","token_url":"`+ts.srv.URL+`"}`)
	r.tokens().Save(&oauth2.Token{AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(-time.Hour)})
	if err := r.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	listTools(t, env, "railway")
	if got := mcpSrv.header("Authorization"); got != "Bearer fresh" {
		t.Errorf("Authorization = %q", got)
	}
	if ts.get("resource") != mcpSrv.srv.URL || ts.get("client_id") != "saved" || ts.get("refresh_token") != "r1" {
		t.Errorf("refresh form = %v", ts.form)
	}
	waitFor(t, "the rotated token to be saved", func() bool {
		return strings.Contains(secretValue(t, env.Secrets, "railway", "token"), `"refresh_token":"rot"`)
	})
}

func TestRailwayConfig(t *testing.T) {
	env, _ := newEnv(t)
	cfg := (&Railway{env: env}).config(&railwaySetup{ClientID: "c", AuthURL: "https://a", TokenURL: "https://t"})
	if cfg.ClientID != "c" || cfg.ClientSecret != "" || cfg.Endpoint.AuthURL != "https://a" || cfg.Endpoint.TokenURL != "https://t" ||
		cfg.Endpoint.AuthStyle != oauth2.AuthStyleInParams || strings.Join(cfg.Scopes, " ") != "openid profile email offline_access workspace:member" {
		t.Errorf("config = %+v", cfg)
	}
}

func TestRegisterClientFailures(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad  request body", http.StatusBadRequest)
	}))
	defer srv.Close()
	if _, err := registerClient(ctx, srv.URL); err == nil || err.Error() != "client registration: 400 Bad Request bad  request body" {
		t.Errorf("status error = %v", err)
	}
	if _, err := registerClient(ctx, "http://127.0.0.1:1/"); err == nil {
		t.Error("unreachable endpoint accepted")
	}
	if _, err := registerClient(ctx, "http://bad\x7furl/"); err == nil {
		t.Error("malformed endpoint accepted")
	}
	// An answer in the 2xx range other than 200 is fine.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"client_id":"c"}`)
	}))
	defer ok.Close()
	if id, err := registerClient(ctx, ok.URL); err != nil || id != "c" {
		t.Errorf("202 = %q %v", id, err)
	}
	// 3xx is not success (the client does not follow a missing Location).
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMultipleChoices)
		io.WriteString(w, `{"client_id":"c"}`)
	}))
	defer redir.Close()
	if _, err := registerClient(ctx, redir.URL); err == nil {
		t.Error("300 accepted")
	}
	// The call stops with its context.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := registerClient(cctx, ok.URL); err == nil {
		t.Error("cancelled context accepted")
	}
}
