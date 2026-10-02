package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/farero-dev/farero/daemon/internal/auth"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/upstream"
	"golang.org/x/oauth2"
)

// railwayMCP is Railway's remote MCP server; its protected resource
// metadata names the authorization server (backboard.railway.com).
const railwayMCP = "https://mcp.railway.com"

var railwayScopes = []string{"openid", "profile", "email", "offline_access", "workspace:member"}

// Railway registers a client with DCR and connects with the device flow
// (Q49). Project tokens are not accepted.
type Railway struct{ env *Env }

func (r *Railway) tokens() auth.Tokens { return auth.Tokens{Store: r.env.Secrets, Plugin: "railway"} }

// railwaySetup is what discovery and registration produce; it is saved so a
// restart does not register again.
type railwaySetup struct {
	Resource      string `json:"resource"`
	ClientID      string `json:"client_id"`
	TokenURL      string `json:"token_url"`
	DeviceAuthURL string `json:"device_auth_url"`
}

func (r *Railway) setup(ctx context.Context) (*railwaySetup, error) {
	if s, err := r.tokens().Value("setup"); err == nil {
		var rs railwaySetup
		if decodeJSON(s, &rs) == nil && rs.ClientID != "" {
			return &rs, nil
		}
	}
	rm, err := auth.FetchResourceMeta(ctx, railwayMCP)
	if err != nil {
		return nil, err
	}
	sm, err := auth.FetchServerMeta(ctx, rm.AuthorizationServers[0])
	if err != nil {
		return nil, err
	}
	if sm.DeviceAuthorizationEndpoint == "" || sm.RegistrationEndpoint == "" {
		return nil, fmt.Errorf("Railway 인증 서버가 device flow 또는 DCR을 지원하지 않음")
	}
	clientID, err := registerClient(ctx, sm.RegistrationEndpoint)
	if err != nil {
		return nil, err
	}
	rs := &railwaySetup{Resource: rm.Resource, ClientID: clientID, TokenURL: sm.TokenEndpoint, DeviceAuthURL: sm.DeviceAuthorizationEndpoint}
	b, _ := json.Marshal(rs)
	r.tokens().SetValue("setup", string(b))
	return rs, nil
}

// registerClient performs RFC 7591 dynamic client registration for a public
// device-flow client.
func registerClient(ctx context.Context, endpoint string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"client_name":                "farero",
		"client_uri":                 "https://github.com/farero-dev/farero-dev.github.io",
		"grant_types":                []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
		"response_types":             []string{},
		"redirect_uris":              []string{"http://127.0.0.1/callback"},
		"token_endpoint_auth_method": "none",
		"scope":                      strings.Join(railwayScopes, " "),
	})
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("client registration: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.ClientID == "" {
		return "", fmt.Errorf("client registration: no client_id in response")
	}
	return out.ClientID, nil
}

func (r *Railway) config(s *railwaySetup) *oauth2.Config {
	return &oauth2.Config{
		ClientID: s.ClientID,
		Endpoint: oauth2.Endpoint{TokenURL: s.TokenURL, DeviceAuthURL: s.DeviceAuthURL, AuthStyle: oauth2.AuthStyleInParams},
		Scopes:   railwayScopes,
	}
}

// Connect implements core.Connector.
func (r *Railway) Connect(ctx context.Context, _ map[string]string, prompt func(ipc.PluginPrompt)) error {
	s, err := r.setup(ctx)
	if err != nil {
		return wrap("railway", err)
	}
	cfg := r.config(s)
	tok, err := auth.DeviceFlow(ctx, cfg, toIPCPrompt(prompt), auth.Resource(s.Resource))
	if err != nil {
		return wrap("railway", err)
	}
	if err := r.tokens().Save(tok); err != nil {
		return err
	}
	p := r.plugin(s, tok)
	label := ""
	if res, err := p.CallTool(ctx, "whoami", json.RawMessage(`{}`)); err == nil && !res.IsError {
		label = firstLine(upstream.ResultText(res), 60)
	}
	return r.env.connected(ctx, p, label)
}

// Restore implements Service.
func (r *Railway) Restore(ctx context.Context) error {
	s, err := r.setup(ctx)
	if err != nil {
		return err
	}
	tok, err := r.tokens().Load()
	if err != nil {
		return err
	}
	return r.env.connected(ctx, r.plugin(s, tok), "")
}

// Disconnect implements core.Connector. The client registration is kept.
func (r *Railway) Disconnect(context.Context) error {
	r.tokens().Clear()
	return nil
}

func (r *Railway) plugin(s *railwaySetup, tok *oauth2.Token) upstream.Plugin {
	ts := r.tokens().Source(r.config(s), tok, s.Resource, r.env.expired("railway"))
	return upstream.NewRemote("railway", s.Resource, auth.HTTPClient(ts, nil), r.env.Version)
}
