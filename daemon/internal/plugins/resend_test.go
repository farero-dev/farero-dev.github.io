package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/auth"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"golang.org/x/oauth2"
)

func resendFixture(t *testing.T) (*Env, *Resend, *fakeMCP) {
	t.Helper()
	env, _ := newEnv(t)
	mcpSrv := newFakeMCP(t)
	old := resendMCP
	resendMCP = mcpSrv.srv.URL
	t.Cleanup(func() { resendMCP = old })
	return env, &Resend{env: env}, mcpSrv
}

func resendMetas(t *testing.T, cimd bool) (resource, issuer *string) {
	t.Helper()
	return stubMeta(t,
		&auth.ResourceMeta{Resource: "https://mcp.resend.example", AuthorizationServers: []string{"https://auth.resend.example", "https://other.example"}}, nil,
		&auth.ServerMeta{AuthorizationEndpoint: "https://auth.resend.example/authorize", TokenEndpoint: "https://auth.resend.example/token", ClientIDMetadataDocumentSupported: cimd}, nil)
}

func TestResendConstants(t *testing.T) {
	// The client id is the metadata document's URL and must never change (Q68).
	if resendClientID != "https://farero-dev.github.io/oauth/client-metadata.json" || resendScope != "full_access" {
		t.Errorf("client id %q scope %q", resendClientID, resendScope)
	}
	if resendMCP != "https://mcp.resend.com/mcp" {
		t.Errorf("endpoint = %q", resendMCP)
	}
}

func TestResendConnect(t *testing.T) {
	env, r, mcpSrv := resendFixture(t)
	resource, issuer := resendMetas(t, true)
	flow := stubLoopback(t, &oauth2.Token{AccessToken: "rs-access", RefreshToken: "rs-refresh"}, nil)
	prompt, prompts := collectPrompt()

	if err := r.Connect(context.Background(), nil, prompt); err != nil {
		t.Fatal(err)
	}
	if *resource != "http://"+strings.TrimPrefix(mcpSrv.srv.URL, "http://") || *issuer != "https://auth.resend.example" {
		t.Errorf("discovery asked %q and %q", *resource, *issuer)
	}
	if flow.cfg.ClientID != resendClientID || flow.cfg.ClientSecret != "" || flow.cfg.Endpoint.AuthURL != "https://auth.resend.example/authorize" ||
		flow.cfg.Endpoint.TokenURL != "https://auth.resend.example/token" || flow.cfg.Endpoint.AuthStyle != oauth2.AuthStyleInParams ||
		len(flow.cfg.Scopes) != 1 || flow.cfg.Scopes[0] != "full_access" {
		t.Errorf("flow config = %+v", flow.cfg)
	}
	if flow.path != "/callback" || flow.authURL.Get("resource") != "https://mcp.resend.example" || flow.authURL.Get("client_id") != resendClientID {
		t.Errorf("flow path %q url %v", flow.path, flow.authURL)
	}
	if len(*prompts) != 1 || (*prompts)[0].URL != "https://auth.example/authorize" {
		t.Errorf("prompts = %+v", *prompts)
	}
	tok, err := r.tokens().Load()
	if err != nil || tok.AccessToken != "rs-access" {
		t.Fatalf("token = %+v %v", tok, err)
	}
	var saved resendSetup
	if err := json.Unmarshal([]byte(secretValue(t, env.Secrets, "resend", "setup")), &saved); err != nil ||
		saved != (resendSetup{Resource: "https://mcp.resend.example", AuthURL: "https://auth.resend.example/authorize", TokenURL: "https://auth.resend.example/token"}) {
		t.Errorf("saved setup = %+v %v", saved, err)
	}
	if st := state(t, env, "resend"); st.Status != model.PluginConnected || st.AccountLabel != "Resend" || st.ConnectedAt.IsZero() {
		t.Errorf("state = %+v", st)
	}
	// The plugin talks to the /mcp endpoint, not the resource origin.
	listTools(t, env, "resend")
	if got := mcpSrv.header("Authorization"); got != "Bearer rs-access" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestResendConnectErrors(t *testing.T) {
	ctx := context.Background()
	noPrompt := func(ipc.PluginPrompt) {}
	t.Run("no CIMD support", func(t *testing.T) {
		env, r, _ := resendFixture(t)
		resendMetas(t, false)
		flow := stubLoopback(t, nil, nil)
		err := r.Connect(ctx, nil, noPrompt)
		if err == nil || !strings.HasPrefix(err.Error(), "resend: ") || !strings.Contains(err.Error(), "Client ID Metadata Document") || flow.calls != 0 {
			t.Errorf("err = %v", err)
		}
		if hasSecret(env.Secrets, "resend", "setup") {
			t.Error("setup saved")
		}
	})
	t.Run("resource metadata", func(t *testing.T) {
		_, r, _ := resendFixture(t)
		stubMeta(t, nil, errors.New("no resource metadata"), nil, nil)
		if err := r.Connect(ctx, nil, noPrompt); err == nil || err.Error() != "resend: no resource metadata" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("server metadata", func(t *testing.T) {
		_, r, _ := resendFixture(t)
		stubMeta(t, &auth.ResourceMeta{Resource: "r", AuthorizationServers: []string{"i"}}, nil, nil, errors.New("no server metadata"))
		if err := r.Connect(ctx, nil, noPrompt); err == nil || err.Error() != "resend: no server metadata" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("denied", func(t *testing.T) {
		env, r, _ := resendFixture(t)
		resendMetas(t, true)
		stubLoopback(t, nil, errors.New("authorization denied: access_denied "))
		err := r.Connect(ctx, nil, noPrompt)
		if err == nil || err.Error() != "resend: authorization denied: access_denied " {
			t.Errorf("err = %v", err)
		}
		if hasSecret(env.Secrets, "resend", "token") || state(t, env, "resend").Status == model.PluginConnected {
			t.Error("state left behind")
		}
	})
	t.Run("save fails", func(t *testing.T) {
		env, r, _ := resendFixture(t)
		resendMetas(t, true)
		stubLoopback(t, &oauth2.Token{AccessToken: "t"}, nil)
		env.Secrets.(*failSecrets).failSet = true
		if err := r.Connect(ctx, nil, noPrompt); err == nil || !strings.Contains(err.Error(), "keychain locked") {
			t.Errorf("err = %v", err)
		}
		if _, ok := env.Core.Plugins().Get("resend"); ok {
			t.Error("plugin registered")
		}
	})
}

func TestResendSetupReuse(t *testing.T) {
	_, r, _ := resendFixture(t)
	ctx := context.Background()
	noMeta(t)
	r.tokens().SetValue("setup", `{"resource":"res","auth_url":"https://a","token_url":"https://t"}`)
	s, err := r.setup(ctx)
	if err != nil || s.Resource != "res" || s.AuthURL != "https://a" || s.TokenURL != "https://t" {
		t.Errorf("setup = %+v %v", s, err)
	}
}

func TestResendSetupDiscoversAgain(t *testing.T) {
	ctx := context.Background()
	// A saved setup without a token endpoint, or a broken one, is discovered again.
	for name, saved := range map[string]string{
		"no token url": `{"resource":"res","auth_url":"https://a"}`,
		"broken":       `nope`,
		"type error":   `{"resource":"res","token_url":"https://t","auth_url":5}`,
	} {
		_, r, _ := resendFixture(t)
		resendMetas(t, true)
		r.tokens().SetValue("setup", saved)
		s, err := r.setup(ctx)
		if err != nil || s.TokenURL != "https://auth.resend.example/token" {
			t.Errorf("%s: setup = %+v %v", name, s, err)
		}
	}
}

func TestResendRestoreAndDisconnect(t *testing.T) {
	env, r, mcpSrv := resendFixture(t)
	ctx := context.Background()
	// Without a saved setup the restore discovers again, which fails offline.
	stubMeta(t, nil, errors.New("offline"), nil, nil)
	if err := r.Restore(ctx); err == nil || err.Error() != "offline" {
		t.Errorf("restored without setup: %v", err)
	}
	noMeta(t)
	r.tokens().SetValue("setup", `{"resource":"res","auth_url":"https://a","token_url":"https://t"}`)
	if err := r.Restore(ctx); err == nil {
		t.Error("restored without token")
	}
	r.tokens().Save(&oauth2.Token{AccessToken: "rs-saved", Expiry: time.Now().Add(time.Hour)})
	if err := r.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if st := state(t, env, "resend"); st.Status != model.PluginConnected || st.AccountLabel != "" {
		t.Errorf("state = %+v", st)
	}
	listTools(t, env, "resend")
	if got := mcpSrv.header("Authorization"); got != "Bearer rs-saved" {
		t.Errorf("Authorization = %q", got)
	}
	if err := r.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if hasSecret(env.Secrets, "resend", "token") {
		t.Error("token survived Disconnect")
	}
}

// Resend rotates refresh tokens: the refresh carries the CIMD client id and
// the resource, and the rotated token is stored.
func TestResendRefreshRotates(t *testing.T) {
	env, r, mcpSrv := resendFixture(t)
	noMeta(t)
	ts := newTokenServer(t)
	r.tokens().SetValue("setup", `{"resource":"https://mcp.resend.example","auth_url":"https://a","token_url":"`+ts.srv.URL+`"}`)
	r.tokens().Save(&oauth2.Token{AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(-time.Hour)})
	if err := r.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	listTools(t, env, "resend")
	if got := mcpSrv.header("Authorization"); got != "Bearer fresh" {
		t.Errorf("Authorization = %q", got)
	}
	if ts.get("resource") != "https://mcp.resend.example" || ts.get("client_id") != resendClientID {
		t.Errorf("refresh form = %v", ts.form)
	}
	waitFor(t, "the rotated token to be saved", func() bool {
		return strings.Contains(secretValue(t, env.Secrets, "resend", "token"), `"refresh_token":"rot"`)
	})
}
