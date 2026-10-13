package plugins

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/auth"
	"github.com/farero-dev/farero/daemon/internal/core"
	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/upstream"
	"github.com/farero-dev/farero/daemon/internal/upstream/devplugin"
)

func TestServicesAndConnectors(t *testing.T) {
	env, _ := newEnv(t)
	svcs := Services(env)
	var names []string
	for name := range svcs {
		names = append(names, name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "github,gmail,railway,resend" {
		t.Fatalf("services = %v", names)
	}
	if g, ok := svcs["github"].(*GitHub); !ok || g.env != env {
		t.Errorf("github = %#v", svcs["github"])
	}
	if r, ok := svcs["railway"].(*Railway); !ok || r.env != env {
		t.Errorf("railway = %#v", svcs["railway"])
	}
	if r, ok := svcs["resend"].(*Resend); !ok || r.env != env {
		t.Errorf("resend = %#v", svcs["resend"])
	}
	if g, ok := svcs["gmail"].(*Gmail); !ok || g.env != env {
		t.Errorf("gmail = %#v", svcs["gmail"])
	}
	conns := Connectors(svcs)
	if len(conns) != len(svcs) {
		t.Fatalf("connectors = %d", len(conns))
	}
	for name, svc := range svcs {
		if conns[name] != core.Connector(svc) {
			t.Errorf("connector %s differs", name)
		}
	}
	if len(Connectors(nil)) != 0 {
		t.Error("connectors of nothing")
	}
}

// fakeService records restores.
type fakeService struct {
	core.Connector
	err   error
	calls int
}

func (f *fakeService) Restore(context.Context) error { f.calls++; return f.err }

func TestRestoreAll(t *testing.T) {
	env, _ := newEnv(t)
	ctx := context.Background()
	for name, status := range map[string]string{
		"ok":           model.PluginConnected,
		"fails":        model.PluginConnected,
		"expired":      model.PluginExpired,
		"disconnected": model.PluginDisconnected,
	} {
		env.Store.SavePlugin(ctx, model.PluginState{Plugin: name, Status: status, AccountLabel: "acct-" + name, ConnectedAt: time.Unix(5000, 0)})
	}
	ok, fails, expired, disc, never := &fakeService{}, &fakeService{err: errors.New("no token")}, &fakeService{}, &fakeService{}, &fakeService{}
	RestoreAll(ctx, env, map[string]Service{"ok": ok, "fails": fails, "expired": expired, "disconnected": disc, "never saved": never})

	if ok.calls != 1 || fails.calls != 1 || expired.calls != 0 || disc.calls != 0 || never.calls != 0 {
		t.Errorf("restore calls: ok %d fails %d expired %d disconnected %d never %d", ok.calls, fails.calls, expired.calls, disc.calls, never.calls)
	}
	if st := state(t, env, "ok"); st.Status != model.PluginConnected {
		t.Errorf("ok = %+v", st)
	}
	// A failed restore shows "token expired" and keeps the account label.
	if st := state(t, env, "fails"); st.Status != model.PluginExpired || st.AccountLabel != "acct-fails" {
		t.Errorf("fails = %+v", st)
	}
	if st := state(t, env, "expired"); st.Status != model.PluginExpired {
		t.Errorf("expired = %+v", st)
	}
	if st := state(t, env, "disconnected"); st.Status != model.PluginDisconnected {
		t.Errorf("disconnected = %+v", st)
	}
}

func TestToIPCPrompt(t *testing.T) {
	var got ipc.PluginPrompt
	toIPCPrompt(func(p ipc.PluginPrompt) { got = p })(auth.Prompt{URL: "https://u", UserCode: "CODE", ExpiresAt: time.Unix(7, 0)})
	if got.URL != "https://u" || got.UserCode != "CODE" || !got.ExpiresAt.Equal(time.Unix(7, 0)) || got.Plugin != "" {
		t.Errorf("prompt = %+v", got)
	}
}

func TestConnectedKeepsOptionsAndTime(t *testing.T) {
	env, _ := newEnv(t)
	ctx := context.Background()
	opts := map[string]string{"read_only": "true"}
	env.Store.SavePlugin(ctx, model.PluginState{Plugin: devplugin.Name, Status: model.PluginExpired, AccountLabel: "old", ConnectedAt: time.Unix(5000, 0), Options: opts})
	p := upstream.NewRemote(devplugin.Name, "http://127.0.0.1:1", nil, "v")

	// Without a label the old label and connection time stay.
	if err := env.connected(ctx, p, ""); err != nil {
		t.Fatal(err)
	}
	st := state(t, env, devplugin.Name)
	if st.Status != model.PluginConnected || st.AccountLabel != "old" || !st.ConnectedAt.Equal(time.Unix(5000, 0)) || st.Options["read_only"] != "true" {
		t.Errorf("without label: %+v", st)
	}
	if got, ok := env.Core.Plugins().Get(devplugin.Name); !ok || got != upstream.Plugin(p) {
		t.Error("plugin not registered")
	}
	// A new label is a new connection.
	if err := env.connected(ctx, p, "new"); err != nil {
		t.Fatal(err)
	}
	st = state(t, env, devplugin.Name)
	if st.AccountLabel != "new" || time.Since(st.ConnectedAt) > time.Minute || st.Options["read_only"] != "true" {
		t.Errorf("with label: %+v", st)
	}
	// A plugin never seen gets a connection time.
	q := upstream.NewRemote("fresh", "http://127.0.0.1:1", nil, "v")
	if err := env.connected(ctx, q, ""); err != nil {
		t.Fatal(err)
	}
	if st := state(t, env, "fresh"); st.ConnectedAt.IsZero() || st.Status != model.PluginConnected {
		t.Errorf("fresh: %+v", st)
	}
}

func TestSetStatus(t *testing.T) {
	env, _ := newEnv(t)
	ctx := context.Background()
	env.Store.SavePlugin(ctx, model.PluginState{Plugin: "x", Status: model.PluginConnected, AccountLabel: "acct", ConnectedAt: time.Unix(5000, 0), Options: map[string]string{"k": "v"}})

	env.setStatus(ctx, "x", model.PluginExpired, "")
	if st := state(t, env, "x"); st.Status != model.PluginExpired || st.AccountLabel != "acct" || st.ConnectedAt.IsZero() || st.Options["k"] != "v" {
		t.Errorf("expired: %+v", st)
	}
	env.setStatus(ctx, "x", model.PluginError, "relabelled")
	if st := state(t, env, "x"); st.Status != model.PluginError || st.AccountLabel != "relabelled" {
		t.Errorf("error: %+v", st)
	}
	// Disconnecting forgets the account but keeps the user's options.
	env.setStatus(ctx, "x", model.PluginDisconnected, "")
	if st := state(t, env, "x"); st.Status != model.PluginDisconnected || st.AccountLabel != "" || !st.ConnectedAt.IsZero() || st.Options["k"] != "v" {
		t.Errorf("disconnected: %+v", st)
	}
}

func TestExpiredCallback(t *testing.T) {
	env, _ := newEnv(t)
	ctx := context.Background()
	env.Store.SavePlugin(ctx, model.PluginState{Plugin: "x", Status: model.PluginConnected, AccountLabel: "acct"})
	env.Core.Plugins().Set(upstream.NewRemote("x", "http://127.0.0.1:1", nil, "v"))
	env.expired("x")(errors.New("refresh failed"))
	if _, ok := env.Core.Plugins().Get("x"); ok {
		t.Error("expired plugin still registered")
	}
	if st := state(t, env, "x"); st.Status != model.PluginExpired || st.AccountLabel != "acct" {
		t.Errorf("state = %+v", st)
	}
}

func TestOption(t *testing.T) {
	env, _ := newEnv(t)
	ctx := context.Background()
	if got := env.option(ctx, "x", "k"); got != "" {
		t.Errorf("unknown plugin = %q", got)
	}
	env.Store.SavePlugin(ctx, model.PluginState{Plugin: "x", Status: model.PluginConnected})
	if got := env.option(ctx, "x", "k"); got != "" {
		t.Errorf("no options = %q", got)
	}
	env.Store.SavePlugin(ctx, model.PluginState{Plugin: "x", Status: model.PluginConnected, Options: map[string]string{"k": "v"}})
	if got := env.option(ctx, "x", "k"); got != "v" {
		t.Errorf("option = %q", got)
	}
	if got := env.option(ctx, "x", "other"); got != "" {
		t.Errorf("missing key = %q", got)
	}
	// A closed store reads as "no option".
	env.Store.Close()
	if got := env.option(ctx, "x", "k"); got != "" {
		t.Errorf("closed store = %q", got)
	}
}

func TestStoreErrorsAreReported(t *testing.T) {
	env, _ := newEnv(t)
	ctx := context.Background()
	env.Store.Close()
	p := upstream.NewRemote("x", "http://127.0.0.1:1", nil, "v")
	if err := env.connected(ctx, p, "l"); err == nil {
		t.Error("connected on a closed store")
	}
	env.setStatus(ctx, "x", model.PluginExpired, "") // must not panic
}

func TestWrapAndDecode(t *testing.T) {
	if wrap("p", nil) != nil {
		t.Error("nil wrapped")
	}
	base := errors.New("boom")
	err := wrap("railway", base)
	if err.Error() != "railway: boom" || !errors.Is(err, base) {
		t.Errorf("wrapped = %v", err)
	}
	var v struct{ A int }
	if err := decodeJSON(`{"A":3}`, &v); err != nil || v.A != 3 {
		t.Errorf("decode = %+v %v", v, err)
	}
	if err := decodeJSON(`nope`, &v); err == nil {
		t.Error("garbage decoded")
	}
}

// A plugin that is not connected is skipped without stopping the others;
// map order is random, so many of each make a stop certain to show.
func TestRestoreAllSkipsAndContinues(t *testing.T) {
	env, _ := newEnv(t)
	ctx := context.Background()
	svcs := map[string]Service{}
	var connected []*fakeService
	for i := 0; i < 20; i++ {
		off, on := fmt.Sprintf("off%d", i), fmt.Sprintf("on%d", i)
		env.Store.SavePlugin(ctx, model.PluginState{Plugin: off, Status: model.PluginDisconnected})
		env.Store.SavePlugin(ctx, model.PluginState{Plugin: on, Status: model.PluginConnected})
		svcs[off] = &fakeService{}
		fs := &fakeService{}
		svcs[on] = fs
		connected = append(connected, fs)
	}
	RestoreAll(ctx, env, svcs)
	for i, fs := range connected {
		if fs.calls != 1 {
			t.Errorf("on%d restored %d times", i, fs.calls)
		}
	}
}
