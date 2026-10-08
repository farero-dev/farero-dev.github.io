package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/farero-dev/farero/daemon/internal/auth"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/upstream"
	"golang.org/x/oauth2"
)

// GitHub endpoints. The remote MCP server accepts the OAuth App's device
// flow token as a Bearer token (M0-4, 검증 결과 9장).
const (
	githubMCP      = "https://api.githubcopilot.com/mcp/"
	githubToolsets = "context,repos,issues,pull_requests,users"
	// githubReadOnlyHeader lists only read tools (docs/remote-server.md).
	githubReadOnlyHeader = "X-MCP-Readonly"
)

var githubEndpoint = oauth2.Endpoint{
	AuthURL:       "https://github.com/login/oauth/authorize",
	TokenURL:      "https://github.com/login/oauth/access_token",
	DeviceAuthURL: "https://github.com/login/device/code",
}

// GitHub connects through the farero-dev OAuth App with the device flow,
// scopes repo and read:org (Q59).
type GitHub struct{ env *Env }

func (g *GitHub) tokens() auth.Tokens { return auth.Tokens{Store: g.env.Secrets, Plugin: "github"} }

func (g *GitHub) config() (*oauth2.Config, error) {
	id := g.env.GitHubClientID
	if v := os.Getenv("FARERO_GITHUB_CLIENT_ID"); v != "" {
		id = v
	}
	if id == "" {
		return nil, fmt.Errorf("GitHub OAuth App client id가 설정되지 않음 (빌드 시 지정하거나 FARERO_GITHUB_CLIENT_ID)")
	}
	return &oauth2.Config{ClientID: id, Endpoint: githubEndpoint, Scopes: []string{"repo", "read:org"}}, nil
}

// Connect implements core.Connector.
func (g *GitHub) Connect(ctx context.Context, _ map[string]string, prompt func(ipc.PluginPrompt)) error {
	cfg, err := g.config()
	if err != nil {
		return err
	}
	tok, err := auth.DeviceFlow(ctx, cfg, toIPCPrompt(prompt))
	if err != nil {
		return wrap("github", err)
	}
	if err := g.tokens().Save(tok); err != nil {
		return err
	}
	login, _ := githubLogin(ctx, tok.AccessToken)
	return g.env.connected(ctx, g.plugin(ctx, cfg, tok), login)
}

// Restore implements Service.
func (g *GitHub) Restore(ctx context.Context) error {
	cfg, err := g.config()
	if err != nil {
		return err
	}
	tok, err := g.tokens().Load()
	if err != nil {
		return err
	}
	return g.env.connected(ctx, g.plugin(ctx, cfg, tok), "")
}

// Reconnect rebuilds the plugin after an option change (read-only switch).
func (g *GitHub) Reconnect(ctx context.Context) error { return g.Restore(ctx) }

// Disconnect implements core.Connector.
func (g *GitHub) Disconnect(context.Context) error {
	g.tokens().Clear()
	return nil
}

func (g *GitHub) plugin(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) upstream.Plugin {
	var ts oauth2.TokenSource = auth.StaticSource(tok)
	if tok.RefreshToken != "" {
		ts = g.tokens().Source(cfg, tok, "", g.env.expired("github"))
	}
	headers := map[string]string{"X-MCP-Toolsets": githubToolsets}
	if g.env.option(ctx, "github", "read_only") == "true" {
		// The remote server ignores unknown headers: "X-MCP-Read-Only"
		// left every write tool listed (M5).
		headers[githubReadOnlyHeader] = "true"
	}
	return upstream.NewRemote("github", githubMCP, auth.HTTPClient(ts, headers), g.env.Version)
}

func githubLogin(ctx context.Context, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", err
	}
	return u.Login, nil
}
