package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/secret"
	"golang.org/x/oauth2"
)

func githubFixture(t *testing.T) (*Env, *failSecrets, *GitHub, *fakeMCP) {
	t.Helper()
	env, sec := newEnv(t)
	env.GitHubClientID = "build-id"
	t.Setenv("FARERO_GITHUB_CLIENT_ID", "")
	mcpSrv := newFakeMCP(t)
	old := githubMCP
	githubMCP = mcpSrv.srv.URL
	t.Cleanup(func() { githubMCP = old })
	return env, sec, &GitHub{env: env}, mcpSrv
}

func githubUserServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh-access" || r.Header.Get("Accept") != "application/vnd.github+json" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := githubUserURL
	githubUserURL = srv.URL
	t.Cleanup(func() { githubUserURL = old })
	return srv
}

func TestGitHubConfig(t *testing.T) {
	env, _ := newEnv(t)
	g := &GitHub{env: env}
	t.Setenv("FARERO_GITHUB_CLIENT_ID", "")
	if _, err := g.config(); err == nil || !strings.Contains(err.Error(), "FARERO_GITHUB_CLIENT_ID") {
		t.Fatalf("no client id: %v", err)
	}
	env.GitHubClientID = "build-id"
	cfg, err := g.config()
	if err != nil || cfg.ClientID != "build-id" {
		t.Fatalf("build id: %+v %v", cfg, err)
	}
	if len(cfg.Scopes) != 2 || cfg.Scopes[0] != "repo" || cfg.Scopes[1] != "read:org" {
		t.Errorf("scopes = %v", cfg.Scopes)
	}
	if cfg.Endpoint != githubEndpoint || cfg.Endpoint.DeviceAuthURL != "https://github.com/login/device/code" ||
		cfg.Endpoint.TokenURL != "https://github.com/login/oauth/access_token" {
		t.Errorf("endpoint = %+v", cfg.Endpoint)
	}
	// The environment variable wins over the build-time id.
	t.Setenv("FARERO_GITHUB_CLIENT_ID", "env-id")
	if cfg, _ := g.config(); cfg.ClientID != "env-id" {
		t.Errorf("env override = %q", cfg.ClientID)
	}
}

func TestGitHubConnect(t *testing.T) {
	env, sec, g, mcpSrv := githubFixture(t)
	githubUserServer(t, 200, `{"login":"seongj-un"}`)
	dev := stubDevice(t, &oauth2.Token{AccessToken: "gh-access", TokenType: "Bearer"}, nil)
	prompt, prompts := collectPrompt()

	if err := g.Connect(context.Background(), nil, prompt); err != nil {
		t.Fatal(err)
	}
	if dev.cfg.ClientID != "build-id" {
		t.Errorf("flow client id = %q", dev.cfg.ClientID)
	}
	if len(*prompts) != 1 || (*prompts)[0].URL != "https://github.com/login/device" ||
		(*prompts)[0].UserCode != "ABCD-1234" || !(*prompts)[0].ExpiresAt.Equal(time.Unix(2000, 0)) {
		t.Errorf("prompts = %+v", *prompts)
	}
	tok, err := g.tokens().Load()
	if err != nil || tok.AccessToken != "gh-access" {
		t.Fatalf("saved token = %+v %v", tok, err)
	}
	if !strings.Contains(secretValue(t, sec, "github", "token"), `"access_token":"gh-access"`) {
		t.Error("token not stored under plugin.github.token")
	}
	st := state(t, env, "github")
	if st.Status != model.PluginConnected || st.AccountLabel != "seongj-un" || time.Since(st.ConnectedAt) > time.Minute || st.ConnectedAt.IsZero() {
		t.Errorf("state = %+v", st)
	}
	listTools(t, env, "github")
	if got := mcpSrv.header("Authorization"); got != "Bearer gh-access" {
		t.Errorf("Authorization = %q", got)
	}
	if got := mcpSrv.header("X-MCP-Toolsets"); got != githubToolsets {
		t.Errorf("toolsets = %q", got)
	}
	if mcpSrv.headerSet(githubReadOnlyHeader) {
		t.Error("read-only header sent without the option")
	}
}

func TestGitHubReadOnlyOption(t *testing.T) {
	env, _, g, mcpSrv := githubFixture(t)
	githubUserServer(t, 200, `{"login":"x"}`)
	ctx := context.Background()
	// Options saved before connecting survive the connection.
	env.Store.SavePlugin(ctx, model.PluginState{Plugin: "github", Status: model.PluginDisconnected, Options: map[string]string{"read_only": "true", "other": "keep"}})
	stubDevice(t, &oauth2.Token{AccessToken: "gh-access"}, nil)
	if err := g.Connect(ctx, nil, func(ipc.PluginPrompt) {}); err != nil {
		t.Fatal(err)
	}
	if st := state(t, env, "github"); st.Options["other"] != "keep" || st.Options["read_only"] != "true" {
		t.Errorf("options = %v", st.Options)
	}
	listTools(t, env, "github")
	if got := mcpSrv.header(githubReadOnlyHeader); got != "true" {
		t.Errorf("read-only header = %q", got)
	}

	// Any other value is not read-only; Reconnect rebuilds the plugin.
	st := state(t, env, "github")
	st.Options["read_only"] = "false"
	env.Store.SavePlugin(ctx, st)
	if err := g.Reconnect(ctx); err != nil {
		t.Fatal(err)
	}
	listTools(t, env, "github")
	if mcpSrv.headerSet(githubReadOnlyHeader) {
		t.Error("read-only header still sent")
	}
	st.Options["read_only"] = "true"
	env.Store.SavePlugin(ctx, st)
	if err := g.Reconnect(ctx); err != nil {
		t.Fatal(err)
	}
	listTools(t, env, "github")
	if got := mcpSrv.header(githubReadOnlyHeader); got != "true" {
		t.Errorf("after reconnect = %q", got)
	}
	if got := state(t, env, "github"); got.AccountLabel != "x" {
		t.Errorf("reconnect changed the label: %q", got.AccountLabel)
	}
}

func TestGitHubConnectErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("no client id", func(t *testing.T) {
		env, _ := newEnv(t)
		t.Setenv("FARERO_GITHUB_CLIENT_ID", "")
		dev := stubDevice(t, nil, nil)
		err := (&GitHub{env: env}).Connect(ctx, nil, func(ipc.PluginPrompt) {})
		if err == nil || !strings.Contains(err.Error(), "client id") || dev.calls != 0 {
			t.Errorf("err = %v, flow calls = %d", err, dev.calls)
		}
	})
	t.Run("denied", func(t *testing.T) {
		env, sec, g, _ := githubFixture(t)
		stubDevice(t, nil, context.DeadlineExceeded)
		err := g.Connect(ctx, nil, func(ipc.PluginPrompt) {})
		if err == nil || err.Error() != "github: "+context.DeadlineExceeded.Error() {
			t.Errorf("err = %v", err)
		}
		if hasSecret(sec, "github", "token") || state(t, env, "github").Status == model.PluginConnected {
			t.Error("a failed connect left state behind")
		}
		if _, ok := env.Core.Plugins().Get("github"); ok {
			t.Error("plugin registered")
		}
	})
	t.Run("save fails", func(t *testing.T) {
		env, sec, g, _ := githubFixture(t)
		stubDevice(t, &oauth2.Token{AccessToken: "gh-access"}, nil)
		sec.failSet = true
		err := g.Connect(ctx, nil, func(ipc.PluginPrompt) {})
		if err == nil || !strings.Contains(err.Error(), "keychain locked") {
			t.Errorf("err = %v", err)
		}
		if _, ok := env.Core.Plugins().Get("github"); ok {
			t.Error("plugin registered without a saved token")
		}
	})
	t.Run("login lookup fails", func(t *testing.T) {
		for name, body := range map[string]string{"bad json": "nope", "empty": `{}`} {
			env, _, g, _ := githubFixture(t)
			githubUserServer(t, 200, body)
			stubDevice(t, &oauth2.Token{AccessToken: "gh-access"}, nil)
			if err := g.Connect(ctx, nil, func(ipc.PluginPrompt) {}); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if st := state(t, env, "github"); st.Status != model.PluginConnected || st.AccountLabel != "" {
				t.Errorf("%s: state = %+v", name, st)
			}
		}
	})
}

func TestGitHubLogin(t *testing.T) {
	githubUserServer(t, 200, `{"login":"octo"}`)
	if got, err := githubLogin(context.Background(), "gh-access"); err != nil || got != "octo" {
		t.Errorf("login = %q %v", got, err)
	}
	if _, err := githubLogin(context.Background(), "wrong"); err == nil {
		t.Error("401 body decoded as a login")
	}
	githubUserURL = "http://127.0.0.1:1/"
	if _, err := githubLogin(context.Background(), "gh-access"); err == nil {
		t.Error("unreachable server accepted")
	}
	// The lookup stops with its context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := githubLogin(ctx, "gh-access"); err == nil {
		t.Error("cancelled context accepted")
	}
}

func TestGitHubRestoreAndDisconnect(t *testing.T) {
	env, sec, g, mcpSrv := githubFixture(t)
	ctx := context.Background()
	if err := g.Restore(ctx); err == nil {
		t.Error("restored without a token")
	}
	g.tokens().Save(&oauth2.Token{AccessToken: "saved", Expiry: time.Now().Add(-time.Hour)})
	if err := g.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	st := state(t, env, "github")
	if st.Status != model.PluginConnected || st.AccountLabel != "" {
		t.Errorf("state = %+v", st)
	}
	// A token without a refresh token is sent as is, even past its expiry.
	listTools(t, env, "github")
	if got := mcpSrv.header("Authorization"); got != "Bearer saved" {
		t.Errorf("Authorization = %q", got)
	}
	// A restore keeps the first connection time.
	first := st.ConnectedAt
	if first.IsZero() {
		t.Fatal("no connection time")
	}
	if err := g.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if !state(t, env, "github").ConnectedAt.Equal(first) {
		t.Error("restore changed the connection time")
	}

	// A token that is not JSON, and a missing client id, fail the restore.
	sec.Set(secret.PluginKey("github", "token"), "garbage")
	if err := g.Restore(ctx); err == nil {
		t.Error("restored a garbage token")
	}
	g.tokens().Save(&oauth2.Token{AccessToken: "saved"})
	env.GitHubClientID = ""
	if err := g.Restore(ctx); err == nil || !strings.Contains(err.Error(), "client id") {
		t.Errorf("no client id: %v", err)
	}
	if err := g.Reconnect(ctx); err == nil {
		t.Error("reconnect without client id")
	}

	if err := g.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if hasSecret(sec, "github", "token") {
		t.Error("token survived Disconnect")
	}
}

func TestGitHubRefreshesExpiredToken(t *testing.T) {
	env, sec, g, mcpSrv := githubFixture(t)
	ts := newTokenServer(t)
	old := githubEndpoint
	githubEndpoint.TokenURL = ts.srv.URL
	t.Cleanup(func() { githubEndpoint = old })

	g.tokens().Save(&oauth2.Token{AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(-time.Hour)})
	if err := g.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	listTools(t, env, "github")
	if got := mcpSrv.header("Authorization"); got != "Bearer fresh" {
		t.Errorf("Authorization = %q", got)
	}
	if ts.get("grant_type") != "refresh_token" || ts.get("refresh_token") != "r1" || ts.get("client_id") != "build-id" {
		t.Errorf("refresh form = %v", ts.form)
	}
	waitFor(t, "the refreshed token to be saved", func() bool {
		return strings.Contains(secretValue(t, sec, "github", "token"), `"access_token":"fresh"`)
	})
}

func TestGitHubTokenExpiryMarksPlugin(t *testing.T) {
	env, _, g, _ := githubFixture(t)
	ts := newTokenServer(t)
	ts.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad_refresh_token"}`))
	})
	old := githubEndpoint
	githubEndpoint.TokenURL = ts.srv.URL
	t.Cleanup(func() { githubEndpoint = old })
	g.tokens().Save(&oauth2.Token{AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(-time.Hour)})
	if err := g.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ := env.Core.Plugins().Get("github")
	p.ListTools(context.Background())
	waitFor(t, "the plugin to expire", func() bool { return state(t, env, "github").Status == model.PluginExpired })
	waitFor(t, "the plugin to be removed", func() bool { _, ok := env.Core.Plugins().Get("github"); return !ok })
}
