package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/store"
)

// Connector starts connecting a plugin (an OAuth flow). It may call prompt
// to show the user a code or URL, and returns once the plugin is connected
// and registered, or on failure.
type Connector interface {
	Connect(ctx context.Context, params map[string]string, prompt func(ipc.PluginPrompt)) error
	Disconnect(ctx context.Context) error
}

// KnownPlugins are the plugins the app shows, in display order.
var KnownPlugins = []string{"github", "railway", "resend", "gmail"}

func (c *Core) snapshot(ctx context.Context) ipc.StateSnapshot {
	return ipc.StateSnapshot{
		Version:   c.version,
		Sessions:  c.sessions.List(),
		Approvals: c.broker.Pending(),
		Plugins:   c.pluginStates(ctx),
		Gateway:   c.GatewayInfo(),
	}
}

func (c *Core) pluginStates(ctx context.Context) []model.PluginState {
	names := append([]string(nil), KnownPlugins...)
	for name := range c.connectors {
		if !contains(names, name) {
			names = append(names, name)
		}
	}
	out := []model.PluginState{}
	for _, name := range names {
		p, err := c.store.Plugin(ctx, name)
		if err != nil {
			continue
		}
		c.mu.Lock()
		if e := c.pluginErr[name]; e != "" {
			// Keep "expired" as is; a failed first connect or a failing
			// upstream shows as an error with its reason.
			p.Error = e
			if p.Status != model.PluginExpired {
				p.Status = model.PluginError
			}
		}
		c.mu.Unlock()
		out = append(out, p)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (c *Core) serveUI(ctx context.Context, conn *ipc.Conn, m ipc.Message) {
	client := c.hub.add()
	c.broker.AttachUI()
	defer func() {
		c.hub.remove(client)
		c.broker.DetachUI()
	}()
	if err := conn.Send(ipc.TypeStateSnapshot, m.ID, c.snapshot(ctx)); err != nil {
		return
	}

	// Writer: forward broadcasts.
	go func() {
		for {
			select {
			case msg := <-client.out:
				if conn.Write(msg) != nil {
					conn.Close()
					return
				}
			case <-client.done:
				conn.Close()
				return
			case <-ctx.Done():
				conn.Close()
				return
			}
		}
	}()

	for {
		req, err := conn.Read()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.log.Debug("ui read", "err", err)
			}
			return
		}
		typ, data, err := c.handleUI(ctx, req)
		if err != nil {
			conn.SendError(req.ID, err)
			continue
		}
		conn.Send(typ, req.ID, data)
	}
}

// handleUI answers one request from the app.
func (c *Core) handleUI(ctx context.Context, m ipc.Message) (string, any, error) {
	switch m.Type {
	case ipc.TypeApprovalResponse:
		r, err := ipc.Decode[ipc.ApprovalResponse](m)
		if err != nil {
			return "", nil, err
		}
		if err := c.broker.Respond(r.ApprovalID, r.Answer); err != nil {
			return "", nil, err
		}
		return ipc.TypeOK, nil, nil

	case ipc.TypeLogQuery:
		f, err := ipc.Decode[store.CallFilter](m)
		if err != nil {
			return "", nil, err
		}
		calls, err := c.store.QueryCalls(ctx, f)
		if err != nil {
			return "", nil, err
		}
		if calls == nil {
			calls = []model.Call{}
		}
		return ipc.TypeLogResult, calls, nil

	case ipc.TypePolicyGet:
		return ipc.TypePolicyState, c.policy.Tools(), nil

	case ipc.TypePolicySet:
		r, err := ipc.Decode[ipc.PolicySet](m)
		if err != nil {
			return "", nil, err
		}
		if err := c.policy.SetOverride(r.Plugin, r.Tool, r.Level); err != nil {
			return "", nil, err
		}
		key := r.Plugin + "_" + r.Tool
		if err := c.store.SetPolicyOverride(ctx, key, c.policy.Overrides()[key]); err != nil {
			return "", nil, err
		}
		c.RefreshTools(ctx)
		return ipc.TypePolicyState, c.policy.Tools(), nil

	case ipc.TypeSessionDelete:
		r, err := ipc.Decode[ipc.SessionRef](m)
		if err != nil {
			return "", nil, err
		}
		if err := c.sessions.Delete(ctx, r.SessionID); err != nil {
			return "", nil, err
		}
		c.policy.EndSession(r.SessionID)
		c.hub.broadcast(ipc.TypeSessionRemoved, r)
		return ipc.TypeOK, nil, nil

	case ipc.TypePluginList:
		return ipc.TypePluginList, c.pluginStates(ctx), nil

	case ipc.TypePluginConnect:
		r, err := ipc.Decode[ipc.PluginConnect](m)
		if err != nil {
			return "", nil, err
		}
		conn, ok := c.connectors[r.Plugin]
		if !ok {
			return "", nil, fmt.Errorf("unknown plugin %q", r.Plugin)
		}
		// OAuth can take minutes; run it in the background and report
		// progress through plugin.prompt / plugin.updated broadcasts.
		c.mu.Lock()
		delete(c.pluginErr, r.Plugin)
		c.mu.Unlock()
		go func() {
			err := conn.Connect(context.WithoutCancel(ctx), r.Params, func(p ipc.PluginPrompt) {
				p.Plugin = r.Plugin
				c.hub.broadcast(ipc.TypePluginPrompt, p)
			})
			if err != nil {
				c.mu.Lock()
				c.pluginErr[r.Plugin] = err.Error()
				c.mu.Unlock()
				c.log.Warn("plugin connect", "plugin", r.Plugin, "err", err)
			}
			c.PluginChanged(context.WithoutCancel(ctx), r.Plugin)
		}()
		return ipc.TypeOK, nil, nil

	case ipc.TypePluginDisconnect:
		r, err := ipc.Decode[ipc.PluginRef](m)
		if err != nil {
			return "", nil, err
		}
		if conn, ok := c.connectors[r.Plugin]; ok {
			if err := conn.Disconnect(ctx); err != nil {
				return "", nil, err
			}
		}
		c.plugins.Remove(r.Plugin)
		c.mu.Lock()
		delete(c.pluginErr, r.Plugin)
		c.mu.Unlock()
		p, _ := c.store.Plugin(ctx, r.Plugin)
		p.Status = model.PluginDisconnected
		p.AccountLabel = ""
		if err := c.store.SavePlugin(ctx, p); err != nil {
			return "", nil, err
		}
		c.PluginChanged(ctx, r.Plugin)
		return ipc.TypeOK, nil, nil

	case ipc.TypePluginSetOption:
		r, err := ipc.Decode[ipc.PluginSetOption](m)
		if err != nil {
			return "", nil, err
		}
		p, err := c.store.Plugin(ctx, r.Plugin)
		if err != nil {
			return "", nil, err
		}
		if p.Options == nil {
			p.Options = map[string]string{}
		}
		p.Options[r.Key] = r.Value
		if err := c.store.SavePlugin(ctx, p); err != nil {
			return "", nil, err
		}
		// Apply the option now only if the plugin is connected; otherwise it
		// takes effect on the next connect.
		_, live := c.plugins.Get(r.Plugin)
		if rc, ok := c.connectors[r.Plugin].(interface{ Reconnect(context.Context) error }); ok && live {
			if err := rc.Reconnect(ctx); err != nil {
				return "", nil, err
			}
		}
		c.PluginChanged(ctx, r.Plugin)
		return ipc.TypeOK, nil, nil

	case ipc.TypeSettingsGet:
		return ipc.TypeSettingsGet, c.settings(ctx), nil

	case ipc.TypeSettingsSet:
		s, err := ipc.Decode[ipc.Settings](m)
		if err != nil {
			return "", nil, err
		}
		if s.ResultLimitBytes > 0 {
			c.store.SetSetting(ctx, settingResultLimit, strconv.Itoa(s.ResultLimitBytes))
		}
		c.store.SetSetting(ctx, settingUpdateCheck, strconv.FormatBool(s.UpdateCheck))
		return ipc.TypeSettingsGet, c.settings(ctx), nil
	}
	if h, ok := c.extraUI[m.Type]; ok {
		return h(ctx, m)
	}
	return "", nil, fmt.Errorf("unknown request %q", m.Type)
}

func (c *Core) settings(ctx context.Context) ipc.Settings {
	upd, _ := c.store.Setting(ctx, settingUpdateCheck)
	return ipc.Settings{ResultLimitBytes: c.resultLimit(ctx), UpdateCheck: upd != "false"}
}

// PluginChanged refreshes the tool list and tells the app.
func (c *Core) PluginChanged(ctx context.Context, plugin string) {
	c.RefreshTools(ctx)
	for _, p := range c.pluginStates(ctx) {
		if p.Plugin == plugin {
			c.hub.broadcast(ipc.TypePluginUpdated, p)
		}
	}
}

// UIHandler handles an extra UI request type registered from outside core
// (agent config installation lives in its own package).
type UIHandler func(ctx context.Context, m ipc.Message) (string, any, error)

// HandleUI registers an extra UI request handler. Call before serving.
func (c *Core) HandleUI(typ string, h UIHandler) {
	if c.extraUI == nil {
		c.extraUI = map[string]UIHandler{}
	}
	c.extraUI[typ] = h
}
