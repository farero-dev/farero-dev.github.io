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

	"github.com/farero-dev/farero/daemon/internal/model"
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
