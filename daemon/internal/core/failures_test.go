package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/store"
	"github.com/farero-dev/farero/daemon/internal/upstream/devplugin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/unix"
)

func TestTimevalTime(t *testing.T) {
	got := timevalTime(unix.Timeval{Sec: 1_700_000_000, Usec: 250_000})
	if want := time.Unix(1_700_000_000, 250_000_000); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Tool uses and refusals are forgotten only after toolUseTTL has passed,
// not at it.
func TestToolUsesPruneAfterTTL(t *testing.T) {
	t0 := time.Now()
	u := &toolUses{
		pending: map[toolUseKey]toolUse{{"claude:s1", "", "Bash", "h"}: {"toolu_1", t0}},
		refused: map[string]refusal{"toolu_2": {call: 1, at: t0}},
	}
	u.pruneLocked(t0.Add(toolUseTTL))
	if len(u.pending) != 1 || len(u.refused) != 1 {
		t.Fatalf("pruned at the TTL: %d pending, %d refused", len(u.pending), len(u.refused))
	}
	u.pruneLocked(t0.Add(toolUseTTL + time.Nanosecond))
	if len(u.pending) != 0 || len(u.refused) != 0 {
		t.Fatalf("kept after the TTL: %d pending, %d refused", len(u.pending), len(u.refused))
	}
}

// When the database fails, the hook still gets its answer and the failure
// is logged instead of lost.
func TestStoreFailuresAreLogged(t *testing.T) {
	var logs syncBuffer
	h := startHarness(t, memStore(t), devTable(), time.Minute, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx := context.Background()
	h.st.Close()

	if m := h.hook("s1", "SessionStart", "", nil, 100); m.Type == "" {
		t.Fatalf("no reply: %+v", m)
	}
	h.core.ranAnyway(ctx, 1)
	gone := model.Session{ID: "claude:gone", Agent: model.AgentClaude, AgentSessionID: "gone"}
	h.core.logCall(ctx, model.Call{SessionID: gone.ID, TS: time.Now(), Kind: model.KindPlugin, Tool: "dev_echo"}, &gone)

	out := logs.String()
	for _, want := range []string{`msg="session update"`, `msg="revive session"`, `msg="audit log"`} {
		if !strings.Contains(out, want) {
			t.Errorf("not logged: %s\n%s", want, out)
		}
	}
}

// brokenPlugin is a connected plugin whose upstream cannot list its tools.
type brokenPlugin struct{ name string }

func (b brokenPlugin) Name() string { return b.name }
func (b brokenPlugin) ListTools(context.Context) ([]*mcp.Tool, error) {
	return nil, errors.New("upstream down")
}
func (b brokenPlugin) CallTool(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
	return nil, errors.New("upstream down")
}
func (b brokenPlugin) Close() error { return nil }

// A plugin that cannot list its tools shows its error and does not hide
// the other plugins' tools, even when it is listed first.
func TestRefreshToolsSkipsFailingPlugin(t *testing.T) {
	h := newHarness(t, time.Minute)
	h.core.Plugins().Set(brokenPlugin{name: "aaa"})
	h.core.RefreshTools(context.Background())
	h.core.mu.Lock()
	var names []string
	for _, x := range h.core.tools {
		names = append(names, x.Name)
	}
	errText := h.core.pluginErr["aaa"]
	h.core.mu.Unlock()
	if !slices.Contains(names, devplugin.Name+"_echo") || errText != "upstream down" {
		t.Fatalf("tools %v, error %q", names, errText)
	}
}

// The app sees every plugin it can connect: the known ones in their order,
// then any other connector once.
func TestPluginStatesListOtherConnectors(t *testing.T) {
	h := newHarness(t, time.Minute)
	h.core.connectors = map[string]Connector{"github": failingConnector{}, "zzz": failingConnector{}}
	var got []string
	for _, p := range h.core.pluginStates(context.Background()) {
		got = append(got, p.Plugin)
	}
	if want := append(slices.Clone(KnownPlugins), "zzz"); !slices.Equal(got, want) {
		t.Fatalf("plugins %v, want %v", got, want)
	}
}

// disconnectFails is a connector whose sign-out the provider refuses.
type disconnectFails struct{}

func (disconnectFails) Connect(context.Context, map[string]string, func(ipc.PluginPrompt)) error {
	return nil
}
func (disconnectFails) Disconnect(context.Context) error { return errors.New("revoke failed") }

// App requests report connector and database failures as errors, and an
// empty log is a list.
func TestUIRequestErrors(t *testing.T) {
	h := newHarness(t, time.Minute)
	h.core.connectors = map[string]Connector{"github": disconnectFails{}, "railway": &optionConnector{}}
	h.core.Plugins().Set(brokenPlugin{name: "railway"}) // connected: an option change reconnects
	h.core.HandleUI("test.echo", func(context.Context, ipc.Message) (string, any, error) { return "test.ok", 7, nil })
	u := h.ui()
	expect := func(id, typ, data string) {
		t.Helper()
		if m := u.reply(id); m.Type != typ || !strings.Contains(string(m.Data), data) {
			t.Fatalf("%s: %s %s, want %s containing %q", id, m.Type, m.Data, typ, data)
		}
	}
	u.conn.Send(ipc.TypeLogQuery, "q0", store.CallFilter{Query: "nothing here"})
	expect("q0", ipc.TypeLogResult, "[]")
	u.conn.Send("test.echo", "e", nil)
	expect("e", "test.ok", "7")
	u.conn.Send(ipc.TypePluginDisconnect, "d", ipc.PluginRef{Plugin: "github"})
	expect("d", ipc.TypeError, "revoke failed")
	u.conn.Send(ipc.TypePluginSetOption, "o", ipc.PluginSetOption{Plugin: "railway", Key: "k", Value: "v"})
	expect("o", ipc.TypeError, "not connected")

	h.st.Close()
	u.conn.Send(ipc.TypeLogQuery, "q1", store.CallFilter{})
	expect("q1", ipc.TypeError, "")
	u.conn.Send(ipc.TypePluginDisconnect, "d1", ipc.PluginRef{Plugin: "gmail"})
	expect("d1", ipc.TypeError, "")
	u.conn.Send(ipc.TypePluginSetOption, "o1", ipc.PluginSetOption{Plugin: "gmail", Key: "k", Value: "v"})
	expect("o1", ipc.TypeError, "")
}

// failingSecrets is a secret store that cannot be read, like a Keychain
// the user refused.
type failingSecrets struct{}

func (failingSecrets) Get(string) (string, error) { return "", errors.New("keychain locked") }
func (failingSecrets) Set(string, string) error   { return errors.New("keychain locked") }
func (failingSecrets) Delete(string) error        { return nil }

// Without the gateway secret the gateway does not start, and the app is
// told why.
func TestGatewaySecretFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := New(ctx, Options{Version: "test", Store: memStore(t), Secrets: failingSecrets{}, Table: devTable(), ApprovalTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.StartGateway(ctx); err == nil || !strings.Contains(err.Error(), "keychain locked") {
		t.Fatalf("start: %v", err)
	}
	if gi := c.GatewayInfo(); gi.Running || gi.Error != "gateway secret: keychain locked" {
		t.Fatalf("gateway info: %+v", gi)
	}
}

// deadlinePlugin records the deadline of its tool listing.
type deadlinePlugin struct {
	brokenPlugin
	deadline chan time.Time
}

func (d deadlinePlugin) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	dl, _ := ctx.Deadline()
	d.deadline <- dl
	return nil, nil
}

// A slow upstream cannot hold the app's tool list request: it gets 30
// seconds.
func TestPluginToolsDeadline(t *testing.T) {
	h := newHarness(t, time.Minute)
	p := deadlinePlugin{brokenPlugin{name: "slow"}, make(chan time.Time, 1)}
	h.core.Plugins().Set(p)
	start := time.Now()
	if _, err := h.core.PluginTools(context.Background(), "slow"); err != nil {
		t.Fatal(err)
	}
	if d := (<-p.deadline).Sub(start); d < 29*time.Second || d > 31*time.Second {
		t.Fatalf("deadline in %v", d)
	}
}

// Correcting a refused call's row logs a database failure instead of
// losing it.
func TestRanAnywayLogsStoreFailure(t *testing.T) {
	var logs syncBuffer
	h := startHarness(t, memStore(t), devTable(), time.Minute, slog.New(slog.NewTextHandler(&logs, nil)))
	h.st.Close()
	h.core.ranAnyway(context.Background(), 1)
	if !strings.Contains(logs.String(), `msg="audit log"`) {
		t.Fatalf("not logged:\n%s", logs.String())
	}
}
