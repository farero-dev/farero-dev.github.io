// Package core wires farerod together: it owns the components, answers IPC
// clients (hooks, headersHelper, the app) and executes gateway calls.
package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/farero-dev/farero/daemon/internal/broker"
	"github.com/farero-dev/farero/daemon/internal/correlate"
	"github.com/farero-dev/farero/daemon/internal/gateway"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/policy"
	"github.com/farero-dev/farero/daemon/internal/secret"
	"github.com/farero-dev/farero/daemon/internal/session"
	"github.com/farero-dev/farero/daemon/internal/store"
	"github.com/farero-dev/farero/daemon/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// GatewayPrefix is how Claude Code names gateway tools in hooks.
const GatewayPrefix = "mcp__farero__"

// Settings keys in the store.
const (
	settingGatewayPort = "gateway.port"
	settingResultLimit = "log.result_limit"
	settingUpdateCheck = "update.check"
)

// Options configure a Core.
type Options struct {
	Version         string
	Store           *store.Store
	Secrets         secret.Store
	Table           *policy.Table
	Log             *slog.Logger
	ApprovalTimeout time.Duration
	// Connectors start plugin connections (OAuth flows). Keyed by plugin.
	Connectors map[string]Connector
}

// Core is farerod's state.
type Core struct {
	version  string
	log      *slog.Logger
	store    *store.Store
	secrets  secret.Store
	sessions *session.Manager
	policy   *policy.Engine
	corr     *correlate.Correlator
	broker   *broker.Broker
	plugins  *upstream.Registry
	gateway  *gateway.Gateway
	hub      *hub

	connectors map[string]Connector
	extraUI    map[string]UIHandler

	mu          sync.Mutex
	gatewayInfo ipc.GatewayInfo
	pluginErr   map[string]string
	toolIndex   map[string]exposedTool // exposed name -> plugin/tool
}

type exposedTool struct {
	plugin string
	tool   string
}

// New builds a Core and loads persisted state.
func New(ctx context.Context, o Options) (*Core, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	overrides, err := o.Store.PolicyOverrides(ctx)
	if err != nil {
		return nil, err
	}
	c := &Core{
		version:    o.Version,
		log:        o.Log,
		store:      o.Store,
		secrets:    o.Secrets,
		policy:     policy.NewEngine(o.Table, overrides),
		corr:       correlate.New(),
		plugins:    upstream.NewRegistry(),
		hub:        newHub(),
		connectors: o.Connectors,
		pluginErr:  map[string]string{},
		toolIndex:  map[string]exposedTool{},
	}
	c.broker = broker.New(c, o.ApprovalTimeout)
	c.sessions, err = session.NewManager(ctx, o.Store, func(s model.Session) {
		c.hub.broadcast(ipc.TypeSessionUpdated, s)
	})
	if err != nil {
		return nil, err
	}
	secretValue, err := secret.GetOrCreate(o.Secrets, secret.KeyGatewaySecret, secret.RandomToken)
	if err != nil {
		return nil, fmt.Errorf("gateway secret: %w", err)
	}
	c.gateway = gateway.New(o.Version, secretValue, c, o.Log)
	return c, nil
}

// Policy exposes the policy engine (dev setup adds rules through the table).
func (c *Core) Policy() *policy.Engine { return c.policy }

// Plugins exposes the plugin registry.
func (c *Core) Plugins() *upstream.Registry { return c.plugins }

// StartGateway starts the MCP listener on the saved port, choosing a free
// one on first run and saving it (Q57).
func (c *Core) StartGateway(ctx context.Context) error {
	saved, _ := c.store.Setting(ctx, settingGatewayPort)
	port, _ := strconv.Atoi(saved)
	bound, err := c.gateway.Start(port)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.gatewayInfo = ipc.GatewayInfo{Port: port, Error: err.Error()}
		return fmt.Errorf("gateway port %d: %w", port, err)
	}
	if saved == "" {
		if err := c.store.SetSetting(ctx, settingGatewayPort, strconv.Itoa(bound)); err != nil {
			return err
		}
	}
	c.gatewayInfo = ipc.GatewayInfo{Running: true, Port: bound, URL: gateway.URL(bound)}
	return nil
}

// GatewayInfo returns the listener state.
func (c *Core) GatewayInfo() ipc.GatewayInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gatewayInfo
}

// Shutdown stops the gateway and closes plugins.
func (c *Core) Shutdown(ctx context.Context) {
	c.gateway.Shutdown(ctx)
	c.plugins.Close()
}

// RefreshTools rebuilds the exposed tool list from connected plugins and
// policy (아키텍처 7-3). Upstream tools missing from the table or blocked
// are not exposed; annotations come from the table, not the upstream.
func (c *Core) RefreshTools(ctx context.Context) {
	var tools []*mcp.Tool
	index := map[string]exposedTool{}
	for _, p := range c.plugins.All() {
		list, err := p.ListTools(ctx)
		c.mu.Lock()
		if err != nil {
			c.pluginErr[p.Name()] = err.Error()
		} else {
			delete(c.pluginErr, p.Name())
		}
		c.mu.Unlock()
		if err != nil {
			c.log.Warn("list tools", "plugin", p.Name(), "err", err)
			continue
		}
		for _, t := range list {
			eff, ok := c.policy.Lookup(p.Name(), t.Name)
			if !ok || eff.Level == policy.LevelBlock {
				continue
			}
			name := policy.ToolKey(p.Name(), t.Name)
			index[name] = exposedTool{plugin: p.Name(), tool: t.Name}
			tools = append(tools, exposeTool(name, t, eff))
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	c.mu.Lock()
	c.toolIndex = index
	c.mu.Unlock()
	c.gateway.SetTools(tools)
}

func exposeTool(name string, t *mcp.Tool, eff policy.Effective) *mcp.Tool {
	readOnly := eff.IsReadOnly(eff.DefaultLevel)
	destructive := eff.Destructive
	openWorld := true
	schema := t.InputSchema
	if schema == nil {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	return &mcp.Tool{
		Name:         name,
		Title:        t.Title,
		Description:  t.Description,
		InputSchema:  schema,
		OutputSchema: t.OutputSchema,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    readOnly,
			DestructiveHint: &destructive,
			OpenWorldHint:   &openWorld,
		},
	}
}

func (c *Core) lookupExposed(name string) (exposedTool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.toolIndex[name]
	return t, ok
}

// ApprovalRequested implements broker.Notifier.
func (c *Core) ApprovalRequested(a model.Approval) {
	c.hub.broadcast(ipc.TypeApprovalRequest, a)
}

// ApprovalCancelled implements broker.Notifier.
func (c *Core) ApprovalCancelled(id, reason string) {
	c.hub.broadcast(ipc.TypeApprovalCancelled, ipc.ApprovalCancelled{ApprovalID: id, Reason: reason})
}

// SweepLoop periodically marks silent sessions with dead processes unknown.
func (c *Core) SweepLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, pid := range c.sessions.Sweep(ctx, processAlive) {
				c.corr.Forget(pid)
			}
		}
	}
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func sessionLabel(s model.Session) string {
	if s.Cwd == "" {
		return s.AgentSessionID
	}
	return filepath.Base(s.Cwd)
}

func (c *Core) resultLimit(ctx context.Context) int {
	v, _ := c.store.Setting(ctx, settingResultLimit)
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	return store.DefaultResultLimit
}

func (c *Core) logCall(ctx context.Context, call model.Call) {
	limit := c.resultLimit(ctx)
	call.ResultBytes = len(call.ResultText)
	call.ResultText = store.TruncateUTF8(call.ResultText, limit)
	if len(call.Input) == 0 {
		call.Input = json.RawMessage(`{}`)
	}
	id, err := c.store.InsertCall(ctx, call)
	if err != nil {
		c.log.Error("audit log", "err", err)
		return
	}
	call.ID = id
	c.hub.broadcast(ipc.TypeCallLogged, trimForBroadcast(call))
}

// Broadcast limits: call.logged tells the app that something happened (for
// the character and live views); the full row stays in the log.
const (
	broadcastInputLimit  = 8 << 10
	broadcastResultLimit = 2 << 10
)

func trimForBroadcast(c model.Call) model.Call {
	if len(c.Input) > broadcastInputLimit {
		c.Input = mustJSON(fmt.Sprintf("(입력 %d bytes, 로그 검색에서 전체 보기)", len(c.Input)))
	}
	c.ResultText = store.TruncateUTF8(c.ResultText, broadcastResultLimit)
	return c
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func joinReasons(r []string) string { return strings.Join(r, ",") }
