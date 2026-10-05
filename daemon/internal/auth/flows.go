package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

// Prompt shows the user what to do: open URL (and enter UserCode for the
// device flow).
type Prompt struct {
	URL       string
	UserCode  string
	ExpiresAt time.Time
}

// DeviceFlow runs the OAuth device authorization grant (RFC 8628): it asks
// for a code, shows it through prompt, and polls until the user approves.
// opts carry extra parameters such as the RFC 8707 resource indicator.
func DeviceFlow(ctx context.Context, cfg *oauth2.Config, prompt func(Prompt), opts ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	da, err := cfg.DeviceAuth(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("device authorization: %w", err)
	}
	link := da.VerificationURIComplete
	if link == "" {
		link = da.VerificationURI
	}
	prompt(Prompt{URL: link, UserCode: da.UserCode, ExpiresAt: da.Expiry})
	tok, err := cfg.DeviceAccessToken(ctx, da, opts...)
	if err != nil {
		return nil, fmt.Errorf("device token: %w", err)
	}
	return tok, nil
}

// LoopbackFlow runs the authorization code grant with PKCE (S256) and a
// loopback redirect on 127.0.0.1 with a free port (RFC 8252 §7.3). It
// sets cfg.RedirectURL to http://127.0.0.1:<port><path>, shows the
// authorization URL through prompt, and waits for the browser to come back.
func LoopbackFlow(ctx context.Context, cfg *oauth2.Config, path string, prompt func(Prompt), opts ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer l.Close()
	c := *cfg
	c.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d%s", l.Addr().(*net.TCPAddr).Port, path)

	state := randomHex(16)
	verifier := oauth2.GenerateVerifier()
	authOpts := append([]oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}, opts...)
	authURL := c.AuthCodeURL(state, authOpts...)

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			res.err = errors.New("state mismatch")
		case q.Get("error") != "":
			res.err = fmt.Errorf("authorization denied: %s %s", q.Get("error"), q.Get("error_description"))
		case q.Get("code") == "":
			res.err = errors.New("no authorization code")
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != nil {
			fmt.Fprintf(w, "<p>farero: 연결하지 못했습니다 (%s). 이 창을 닫아도 됩니다.</p>", html.EscapeString(res.err.Error()))
		} else {
			fmt.Fprint(w, "<p>farero: 연결했습니다. 이 창을 닫아도 됩니다.</p>")
		}
		select {
		case done <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(l)
	defer srv.Close()

	prompt(Prompt{URL: authURL, ExpiresAt: time.Now().Add(10 * time.Minute)})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		return nil, fmt.Errorf("authorization not completed: %w", ctx.Err())
	}
	if res.err != nil {
		return nil, res.err
	}
	tctx := context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	exOpts := append([]oauth2.AuthCodeOption{oauth2.VerifierOption(verifier)}, opts...)
	tok, err := c.Exchange(tctx, res.code, exOpts...)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	return tok, nil
}

// Resource is the RFC 8707 resource indicator option the MCP authorization
// spec requires on authorization and token requests.
func Resource(uri string) oauth2.AuthCodeOption { return oauth2.SetAuthURLParam("resource", uri) }

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
