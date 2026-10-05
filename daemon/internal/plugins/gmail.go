package plugins

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/farero-dev/farero/daemon/internal/auth"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/upstream/gmail"
	"golang.org/x/oauth2"
)

var googleEndpoint = oauth2.Endpoint{
	AuthURL:  "https://accounts.google.com/o/oauth2/auth",
	TokenURL: "https://oauth2.googleapis.com/token",
}

// Gmail uses the OAuth client the user created in their own GCP project
// (Q18): a Desktop app client, loopback redirect and PKCE, scope
// gmail.readonly.
type Gmail struct{ env *Env }

func (g *Gmail) tokens() auth.Tokens { return auth.Tokens{Store: g.env.Secrets, Plugin: "gmail"} }

type gmailClient struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func (g *Gmail) config(c gmailClient) *oauth2.Config {
	return &oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, Endpoint: googleEndpoint, Scopes: []string{gmail.Scope}}
}

func (g *Gmail) client() (gmailClient, error) {
	var c gmailClient
	s, err := g.tokens().Value("client")
	if err != nil {
		return c, err
	}
	err = decodeJSON(s, &c)
	return c, err
}

// Connect implements core.Connector. params: client_id, client_secret
// (optional when already saved).
func (g *Gmail) Connect(ctx context.Context, params map[string]string, prompt func(ipc.PluginPrompt)) error {
	c, _ := g.client()
	if params["client_id"] != "" {
		c = gmailClient{ClientID: params["client_id"], ClientSecret: params["client_secret"]}
	}
	if c.ClientID == "" {
		return errors.New("gmail: GCP OAuth 클라이언트 ID를 입력해야 함")
	}
	cfg := g.config(c)
	tok, err := auth.LoopbackFlow(ctx, cfg, "/callback", toIPCPrompt(prompt),
		oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	if err != nil {
		return wrap("gmail", err)
	}
	b, _ := json.Marshal(c)
	if err := g.tokens().SetValue("client", string(b)); err != nil {
		return err
	}
	if err := g.tokens().Save(tok); err != nil {
		return err
	}
	p := g.plugin(cfg, tok)
	email, _ := p.Email(ctx)
	return g.env.connected(ctx, p, email)
}

// Restore implements Service.
func (g *Gmail) Restore(ctx context.Context) error {
	c, err := g.client()
	if err != nil {
		return err
	}
	tok, err := g.tokens().Load()
	if err != nil {
		return err
	}
	return g.env.connected(ctx, g.plugin(g.config(c), tok), "")
}

// Disconnect implements core.Connector. The user's client settings are kept
// so reconnecting does not ask for them again.
func (g *Gmail) Disconnect(context.Context) error {
	g.tokens().Clear()
	return nil
}

func (g *Gmail) plugin(cfg *oauth2.Config, tok *oauth2.Token) *gmail.Plugin {
	ts := g.tokens().Source(cfg, tok, "", g.env.expired("gmail"))
	return gmail.New(auth.HTTPClient(ts, nil))
}
