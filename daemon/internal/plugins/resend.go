package plugins

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/farero-dev/farero/daemon/internal/auth"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/upstream"
	"golang.org/x/oauth2"
)

const (
	resendMCP = "https://mcp.resend.com/mcp"
	// resendClientID is farero's Client ID Metadata Document, served by the
	// project's GitHub Pages (Q68). It is the client_id, so it must never
	// change. The document is oauth/client-metadata.json in this repo.
	resendClientID = "https://farero-dev.github.io/oauth/client-metadata.json"
	resendScope    = "full_access" // emails:send cannot read (Q60)
)

// Resend connects with OAuth: CIMD client id, PKCE, loopback redirect.
type Resend struct{ env *Env }

func (r *Resend) tokens() auth.Tokens { return auth.Tokens{Store: r.env.Secrets, Plugin: "resend"} }

type resendSetup struct {
	Resource string `json:"resource"`
	AuthURL  string `json:"auth_url"`
	TokenURL string `json:"token_url"`
}

func (r *Resend) setup(ctx context.Context) (*resendSetup, error) {
	if s, err := r.tokens().Value("setup"); err == nil {
		var rs resendSetup
		if decodeJSON(s, &rs) == nil && rs.TokenURL != "" {
			return &rs, nil
		}
	}
	rm, err := auth.FetchResourceMeta(ctx, resendMCP)
	if err != nil {
		return nil, err
	}
	sm, err := auth.FetchServerMeta(ctx, rm.AuthorizationServers[0])
	if err != nil {
		return nil, err
	}
	if !sm.ClientIDMetadataDocumentSupported {
		return nil, fmt.Errorf("Resend 인증 서버가 Client ID Metadata Document를 지원하지 않음")
	}
	rs := &resendSetup{Resource: rm.Resource, AuthURL: sm.AuthorizationEndpoint, TokenURL: sm.TokenEndpoint}
	b, _ := json.Marshal(rs)
	r.tokens().SetValue("setup", string(b))
	return rs, nil
}

func (r *Resend) config(s *resendSetup) *oauth2.Config {
	return &oauth2.Config{
		ClientID: resendClientID,
		Endpoint: oauth2.Endpoint{AuthURL: s.AuthURL, TokenURL: s.TokenURL, AuthStyle: oauth2.AuthStyleInParams},
		Scopes:   []string{resendScope},
	}
}

// Connect implements core.Connector.
func (r *Resend) Connect(ctx context.Context, _ map[string]string, prompt func(ipc.PluginPrompt)) error {
	s, err := r.setup(ctx)
	if err != nil {
		return wrap("resend", err)
	}
	tok, err := auth.LoopbackFlow(ctx, r.config(s), "/callback", toIPCPrompt(prompt), auth.Resource(s.Resource))
	if err != nil {
		return wrap("resend", err)
	}
	if err := r.tokens().Save(tok); err != nil {
		return err
	}
	return r.env.connected(ctx, r.plugin(s, tok), "Resend")
}

// Restore implements Service.
func (r *Resend) Restore(ctx context.Context) error {
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

// Disconnect implements core.Connector.
func (r *Resend) Disconnect(context.Context) error {
	r.tokens().Clear()
	return nil
}

func (r *Resend) plugin(s *resendSetup, tok *oauth2.Token) upstream.Plugin {
	ts := r.tokens().Source(r.config(s), tok, s.Resource, r.env.expired("resend"))
	// The resource identifier is the origin (https://mcp.resend.com) but the
	// MCP endpoint is /mcp.
	return upstream.NewRemote("resend", resendMCP, auth.HTTPClient(ts, nil), r.env.Version)
}
