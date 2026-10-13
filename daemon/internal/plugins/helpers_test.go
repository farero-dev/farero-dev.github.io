package plugins

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/auth"
	"github.com/farero-dev/farero/daemon/internal/core"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/policy"
	"github.com/farero-dev/farero/daemon/internal/secret"
	"github.com/farero-dev/farero/daemon/internal/store"
	"github.com/farero-dev/farero/daemon/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

// failSecrets wraps a secret store and fails writes on demand.
type failSecrets struct {
	secret.Store
	failSet bool
}

func (f *failSecrets) Set(key, v string) error {
	if f.failSet {
		return errors.New("keychain locked")
	}
	return f.Store.Set(key, v)
}

// newEnv builds an Env on an in-memory database, an in-memory secret store
// and a Core with no connectors.
func newEnv(t *testing.T) (*Env, *failSecrets) {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sec := &failSecrets{Store: secret.NewMemoryStore()}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c, err := core.New(context.Background(), core.Options{Store: st, Secrets: sec, Table: policy.DefaultTable(), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Plugins().Close() })
	return &Env{Core: c, Store: st, Secrets: sec, Log: log, Version: "9.9.9"}, sec
}

// fakeMCP is an MCP server with a whoami tool that records the headers of
// the last request.
type fakeMCP struct {
	srv *httptest.Server
	mu  sync.Mutex
	hdr http.Header
	// whoami is the tool's text; whoamiErr makes it a tool error.
	whoami    string
	whoamiErr bool
}

func newFakeMCP(t *testing.T) *fakeMCP {
	t.Helper()
	f := &fakeMCP{whoami: "**Seong** (@seongj-un)\nWorkspaces: 1"}
	s := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "whoami", Description: "who"}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.whoamiErr {
			return upstream.ErrorResult("not signed in"), nil, nil
		}
		return upstream.TextResult(f.whoami), nil, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hdr = r.Header.Clone()
		f.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	// Open event streams from plugin sessions would block Close.
	t.Cleanup(func() { f.srv.CloseClientConnections(); f.srv.Close() })
	return f
}

func (f *fakeMCP) header(k string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hdr.Get(k)
}

func (f *fakeMCP) headerSet(k string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.hdr[http.CanonicalHeaderKey(k)]
	return ok
}

// listTools forces a request to the plugin registered under name, so the
// fake sees its headers.
func listTools(t *testing.T, env *Env, name string) []*mcp.Tool {
	t.Helper()
	p, ok := env.Core.Plugins().Get(name)
	if !ok {
		t.Fatalf("plugin %q not registered", name)
	}
	tools, err := p.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	return tools
}

// flowCall is what a stubbed OAuth flow was asked to do.
type flowCall struct {
	cfg      oauth2.Config
	path     string
	authURL  url.Values
	prompted bool
	calls    int
}

func stubLoopback(t *testing.T, tok *oauth2.Token, err error) *flowCall {
	t.Helper()
	fc := &flowCall{}
	old := loopbackFlow
	loopbackFlow = func(ctx context.Context, cfg *oauth2.Config, path string, prompt func(auth.Prompt), opts ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
		fc.calls++
		fc.cfg, fc.path = *cfg, path
		u, _ := url.Parse(cfg.AuthCodeURL("state", opts...))
		fc.authURL = u.Query()
		prompt(auth.Prompt{URL: "https://auth.example/authorize", ExpiresAt: time.Unix(1000, 0)})
		fc.prompted = true
		return tok, err
	}
	t.Cleanup(func() { loopbackFlow = old })
	return fc
}

func stubDevice(t *testing.T, tok *oauth2.Token, err error) *flowCall {
	t.Helper()
	fc := &flowCall{}
	old := deviceFlow
	deviceFlow = func(ctx context.Context, cfg *oauth2.Config, prompt func(auth.Prompt), opts ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
		fc.calls++
		fc.cfg = *cfg
		prompt(auth.Prompt{URL: "https://github.com/login/device", UserCode: "ABCD-1234", ExpiresAt: time.Unix(2000, 0)})
		fc.prompted = true
		return tok, err
	}
	t.Cleanup(func() { deviceFlow = old })
	return fc
}

func stubMeta(t *testing.T, rm *auth.ResourceMeta, rmErr error, sm *auth.ServerMeta, smErr error) (resource, issuer *string) {
	t.Helper()
	resource, issuer = new(string), new(string)
	oldR, oldS := fetchResourceMeta, fetchServerMeta
	fetchResourceMeta = func(_ context.Context, r string) (*auth.ResourceMeta, error) {
		*resource = r
		return rm, rmErr
	}
	fetchServerMeta = func(_ context.Context, i string) (*auth.ServerMeta, error) {
		*issuer = i
		return sm, smErr
	}
	t.Cleanup(func() { fetchResourceMeta, fetchServerMeta = oldR, oldS })
	return resource, issuer
}

func noMeta(t *testing.T) {
	t.Helper()
	oldR, oldS := fetchResourceMeta, fetchServerMeta
	fetchResourceMeta = func(context.Context, string) (*auth.ResourceMeta, error) {
		t.Error("discovery ran")
		return nil, errors.New("unexpected")
	}
	fetchServerMeta = func(context.Context, string) (*auth.ServerMeta, error) {
		t.Error("discovery ran")
		return nil, errors.New("unexpected")
	}
	t.Cleanup(func() { fetchResourceMeta, fetchServerMeta = oldR, oldS })
}

// collectPrompt returns a prompt callback and the prompts it received.
func collectPrompt() (func(ipc.PluginPrompt), *[]ipc.PluginPrompt) {
	var got []ipc.PluginPrompt
	return func(p ipc.PluginPrompt) { got = append(got, p) }, &got
}

func state(t *testing.T, env *Env, name string) model.PluginState {
	t.Helper()
	st, err := env.Store.Plugin(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func secretValue(t *testing.T, s secret.Store, plugin, what string) string {
	t.Helper()
	v, err := s.Get(secret.PluginKey(plugin, what))
	if err != nil {
		t.Fatalf("secret %s.%s: %v", plugin, what, err)
	}
	return v
}

func hasSecret(s secret.Store, plugin, what string) bool {
	_, err := s.Get(secret.PluginKey(plugin, what))
	return err == nil
}

// tokenServer answers refresh_token grants and records the form.
type tokenServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	form url.Values
}

func newTokenServer(t *testing.T) *tokenServer {
	t.Helper()
	ts := &tokenServer{}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		ts.mu.Lock()
		ts.form = r.PostForm
		ts.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"fresh","token_type":"Bearer","refresh_token":"rot","expires_in":3600}`))
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

func (ts *tokenServer) get(k string) string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.form.Get(k)
}

// waitFor polls cond for up to 5 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
