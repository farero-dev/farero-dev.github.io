// Package plugins connects the four MVP plugins (F-07): it runs each
// service's OAuth flow, keeps tokens in the secret store, builds the
// upstream plugin and registers it with core.
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/farero-dev/farero/daemon/internal/auth"
	"github.com/farero-dev/farero/daemon/internal/core"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/secret"
	"github.com/farero-dev/farero/daemon/internal/store"
	"github.com/farero-dev/farero/daemon/internal/upstream"
)

// Env is what the services need from farerod.
type Env struct {
	Core    *core.Core
	Store   *store.Store
	Secrets secret.Store
	Log     *slog.Logger
	Version string
	// GitHubClientID is the farero-dev GitHub OAuth App's client id (public;
	// set at build time or with FARERO_GITHUB_CLIENT_ID).
	GitHubClientID string
}

// Service is one plugin's connector.
type Service interface {
	core.Connector
	// Restore rebuilds the plugin from the saved token after a restart.
	Restore(ctx context.Context) error
}

// Services returns the connectors keyed by plugin id.
func Services(env *Env) map[string]Service {
	return map[string]Service{
		"github":  &GitHub{env: env},
		"railway": &Railway{env: env},
		"resend":  &Resend{env: env},
		"gmail":   &Gmail{env: env},
	}
}

// Connectors adapts Services to core's map type.
func Connectors(s map[string]Service) map[string]core.Connector {
	out := make(map[string]core.Connector, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}

// RestoreAll reconnects every plugin that was connected before a restart.
func RestoreAll(ctx context.Context, env *Env, s map[string]Service) {
	for name, svc := range s {
		p, err := env.Store.Plugin(ctx, name)
		if err != nil || p.Status != model.PluginConnected {
			continue
		}
		if err := svc.Restore(ctx); err != nil {
			env.Log.Warn("restore plugin", "plugin", name, "err", err)
			env.setStatus(ctx, name, model.PluginExpired, "")
		}
	}
}

func toIPCPrompt(prompt func(ipc.PluginPrompt)) func(auth.Prompt) {
	return func(p auth.Prompt) {
		prompt(ipc.PluginPrompt{URL: p.URL, UserCode: p.UserCode, ExpiresAt: p.ExpiresAt})
	}
}

// connected registers a plugin and records it as connected, keeping any
// options the user set.
func (env *Env) connected(ctx context.Context, p upstream.Plugin, label string) error {
	env.Core.Plugins().Set(p)
	st, err := env.Store.Plugin(ctx, p.Name())
	if err != nil {
		return err
	}
	st.Status = model.PluginConnected
	if label != "" {
		st.AccountLabel = label
	}
	if st.ConnectedAt.IsZero() || label != "" {
		st.ConnectedAt = time.Now()
	}
	return env.Store.SavePlugin(ctx, st)
}

func (env *Env) setStatus(ctx context.Context, name, status, label string) {
	st, err := env.Store.Plugin(ctx, name)
	if err != nil {
		return
	}
	st.Status = status
	if status == model.PluginDisconnected {
		st.AccountLabel = ""
		st.ConnectedAt = time.Time{}
	}
	if label != "" {
		st.AccountLabel = label
	}
	env.Store.SavePlugin(ctx, st)
}

// expired is called when a token can no longer be refreshed: the plugin is
// removed from the gateway and shown as "토큰 만료" until reconnected.
func (env *Env) expired(name string) func(error) {
	return func(err error) {
		ctx := context.Background()
		env.Log.Warn("plugin token expired", "plugin", name, "err", err)
		env.Core.Plugins().Remove(name)
		env.setStatus(ctx, name, model.PluginExpired, "")
		env.Core.PluginChanged(ctx, name)
	}
}

func (env *Env) option(ctx context.Context, plugin, key string) string {
	st, err := env.Store.Plugin(ctx, plugin)
	if err != nil || st.Options == nil {
		return ""
	}
	return st.Options[key]
}

// firstLine trims tool output to a short account label.
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}

func decodeJSON(s string, v any) error { return json.Unmarshal([]byte(s), v) }

var errNotConfigured = errors.New("not configured")

func wrap(plugin string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", plugin, err)
}
