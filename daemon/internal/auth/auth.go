// Package auth runs the OAuth flows farerod uses to connect plugins (아키텍처
// 9장) and keeps the resulting tokens in the secret store.
//
// Tokens belong to farerod only. They are never passed through from an agent
// and never logged (기능 명세서 5-5).
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/farero-dev/farero/daemon/internal/secret"
	"golang.org/x/oauth2"
)

// httpClient is used for discovery and token requests.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// ServerMeta is the subset of RFC 8414 authorization server metadata farero
// needs, including the device authorization endpoint (RFC 8628).
type ServerMeta struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	DeviceAuthorizationEndpoint       string   `json:"device_authorization_endpoint"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	ClientIDMetadataDocumentSupported bool     `json:"client_id_metadata_document_supported"`
	ScopesSupported                   []string `json:"scopes_supported"`
}

// ResourceMeta is RFC 9728 protected resource metadata.
type ResourceMeta struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

func getJSON(ctx context.Context, u string, v any) error {
	if err := requireHTTPS(u); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

func requireHTTPS(u string) error {
	p, err := url.Parse(u)
	if err != nil {
		return err
	}
	if p.Scheme != "https" {
		return fmt.Errorf("%s: only https is allowed", u)
	}
	return nil
}

// FetchServerMeta reads an authorization server's metadata from its issuer.
func FetchServerMeta(ctx context.Context, issuer string) (*ServerMeta, error) {
	iss, err := url.Parse(issuer)
	if err != nil {
		return nil, err
	}
	// RFC 8414 §3: insert the well-known path before any issuer path.
	u := *iss
	u.Path = "/.well-known/oauth-authorization-server" + strings.TrimSuffix(iss.Path, "/")
	var m ServerMeta
	if err := getJSON(ctx, u.String(), &m); err != nil {
		return nil, err
	}
	if m.TokenEndpoint == "" {
		return nil, fmt.Errorf("%s: metadata has no token_endpoint", issuer)
	}
	for _, e := range []string{m.TokenEndpoint, m.AuthorizationEndpoint, m.RegistrationEndpoint, m.DeviceAuthorizationEndpoint} {
		if e != "" {
			if err := requireHTTPS(e); err != nil {
				return nil, err
			}
		}
	}
	return &m, nil
}

// FetchResourceMeta reads an MCP server's protected resource metadata.
func FetchResourceMeta(ctx context.Context, resource string) (*ResourceMeta, error) {
	r, err := url.Parse(resource)
	if err != nil {
		return nil, err
	}
	u := *r
	u.Path = "/.well-known/oauth-protected-resource" + strings.TrimSuffix(r.Path, "/")
	var m ResourceMeta
	if err := getJSON(ctx, u.String(), &m); err != nil {
		// Some servers only publish the root document.
		u.Path = "/.well-known/oauth-protected-resource"
		if err2 := getJSON(ctx, u.String(), &m); err2 != nil {
			return nil, err
		}
	}
	if len(m.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("%s: no authorization_servers in resource metadata", resource)
	}
	return &m, nil
}

// --- token storage ---

// Tokens stores one plugin's OAuth token (and client registration) in the
// secret store.
type Tokens struct {
	Store  secret.Store
	Plugin string
}

func (t Tokens) key(what string) string { return secret.PluginKey(t.Plugin, what) }

// Load returns the saved token.
func (t Tokens) Load() (*oauth2.Token, error) {
	s, err := t.Store.Get(t.key("token"))
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(s), &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// Save stores a token.
func (t Tokens) Save(tok *oauth2.Token) error {
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	return t.Store.Set(t.key("token"), string(b))
}

// Value loads an extra value saved for the plugin (client id, user config).
func (t Tokens) Value(what string) (string, error) { return t.Store.Get(t.key(what)) }

// SetValue saves an extra value for the plugin.
func (t Tokens) SetValue(what, v string) error { return t.Store.Set(t.key(what), v) }

// Clear removes everything stored for the plugin.
func (t Tokens) Clear(extra ...string) {
	t.Store.Delete(t.key("token"))
	for _, w := range extra {
		t.Store.Delete(t.key(w))
	}
}

// ErrExpired means the token could not be refreshed: the user must connect
// the plugin again.
var ErrExpired = errors.New("token expired")

// Source returns a token source that refreshes through cfg and saves every
// new token (Resend rotates refresh tokens on each refresh). resource, when
// set, is sent on refresh requests as the MCP authorization spec requires
// (x/oauth2's own refresh cannot add it). onExpired is called once when
// refreshing fails.
func (t Tokens) Source(cfg *oauth2.Config, tok *oauth2.Token, resource string, onExpired func(error)) oauth2.TokenSource {
	r := &refresher{cfg: cfg, resource: resource, refresh: tok.RefreshToken}
	return &savingSource{base: oauth2.ReuseTokenSource(tok, r), last: tok.AccessToken, tokens: t, onExpired: onExpired}
}

// refresher performs refresh_token grants.
type refresher struct {
	cfg      *oauth2.Config
	resource string
	mu       sync.Mutex
	refresh  string
}

func (r *refresher) setRefresh(rt string) {
	r.mu.Lock()
	r.refresh = rt
	r.mu.Unlock()
}

func (r *refresher) Token() (*oauth2.Token, error) {
	r.mu.Lock()
	rt := r.refresh
	r.mu.Unlock()
	if rt == "" {
		return nil, errors.New("access token expired and there is no refresh token")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {r.cfg.ClientID}}
	if r.cfg.ClientSecret != "" {
		form.Set("client_secret", r.cfg.ClientSecret)
	}
	if r.resource != "" {
		form.Set("resource", r.resource)
	}
	req, err := http.NewRequest(http.MethodPost, r.cfg.Endpoint.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr struct {
		AccessToken  string      `json:"access_token"`
		TokenType    string      `json:"token_type"`
		RefreshToken string      `json:"refresh_token"`
		ExpiresIn    json.Number `json:"expires_in"`
		Error        string      `json:"error"`
		ErrorDesc    string      `json:"error_description"`
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		v, _ := url.ParseQuery(string(body))
		tr.AccessToken, tr.TokenType, tr.RefreshToken = v.Get("access_token"), v.Get("token_type"), v.Get("refresh_token")
		tr.ExpiresIn, tr.Error, tr.ErrorDesc = json.Number(v.Get("expires_in")), v.Get("error"), v.Get("error_description")
	} else {
		_ = json.Unmarshal(body, &tr)
	}
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		if tr.Error == "" {
			tr.Error = resp.Status
		}
		return nil, fmt.Errorf("refresh: %s %s", tr.Error, tr.ErrorDesc)
	}
	tok := &oauth2.Token{AccessToken: tr.AccessToken, TokenType: tr.TokenType, RefreshToken: tr.RefreshToken}
	if tok.RefreshToken == "" {
		tok.RefreshToken = rt
	}
	if n, err := tr.ExpiresIn.Int64(); err == nil && n > 0 {
		tok.Expiry = time.Now().Add(time.Duration(n) * time.Second)
	}
	r.setRefresh(tok.RefreshToken)
	return tok, nil
}

// StaticSource returns a token source for a token that cannot be refreshed.
func StaticSource(tok *oauth2.Token) oauth2.TokenSource { return oauth2.StaticTokenSource(tok) }

type savingSource struct {
	mu        sync.Mutex
	base      oauth2.TokenSource
	last      string
	tokens    Tokens
	onExpired func(error)
	expired   bool
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	tok, err := s.base.Token()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if !s.expired && s.onExpired != nil {
			s.expired = true
			go s.onExpired(err)
		}
		return nil, fmt.Errorf("%w: %v", ErrExpired, err)
	}
	if tok.AccessToken != s.last {
		s.last = tok.AccessToken
		_ = s.tokens.Save(tok)
	}
	return tok, nil
}

// HTTPClient returns an HTTP client that authenticates with ts and adds the
// given static headers.
func HTTPClient(ts oauth2.TokenSource, headers map[string]string) *http.Client {
	base := http.DefaultTransport
	if len(headers) > 0 {
		base = headerTransport{base: http.DefaultTransport, headers: headers}
	}
	return &http.Client{Transport: &oauth2.Transport{Source: ts, Base: base}}
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.headers {
		r.Header.Set(k, v)
	}
	return h.base.RoundTrip(r)
}
