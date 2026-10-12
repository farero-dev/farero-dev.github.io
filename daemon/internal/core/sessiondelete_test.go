package core

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/ipc"
	"github.com/farero-dev/farero/daemon/internal/model"
	"github.com/farero-dev/farero/daemon/internal/upstream"
	"github.com/farero-dev/farero/daemon/internal/upstream/devplugin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// deleteSession deletes a session from the app and returns when.
func (u *fakeUI) deleteSession(id string) time.Time {
	u.t.Helper()
	u.conn.Send(ipc.TypeSessionDelete, "del-"+id, ipc.SessionRef{SessionID: id})
	if m := u.reply("del-" + id); m.Type != ipc.TypeOK {
		u.t.Fatalf("session.delete: %s %s", m.Type, m.Data)
	}
	return time.Now()
}

// recreated waits for a session.updated about id for a session created
// after the given time.
func (u *fakeUI) recreated(id string, after time.Time) model.Session {
	u.t.Helper()
	for {
		s, _ := ipc.Decode[model.Session](u.next(ipc.TypeSessionUpdated))
		if s.ID == id && s.StartedAt.After(after) {
			return s
		}
	}
}

// The notch can delete a session whose agent still runs. Its next hook
// event creates it again, and a tainted one stays tainted: the agent's
// context still holds the untrusted content (Q39).
func TestDeletedLiveSessionStaysTainted(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	a := h.agent("s1", 100)
	a.call("dev_taint_sim", map[string]any{"text": "x"})
	if s, _ := h.core.sessions.Get("claude:s1"); !s.Tainted {
		t.Fatal("session not tainted")
	}
	deleted := u.deleteSession("claude:s1")

	ch := a.callAsync("dev_write_sim", map[string]any{"text": "y"})
	if s := u.recreated("claude:s1", deleted); !s.Tainted {
		t.Fatalf("app told: %+v", s)
	}
	ap := u.approval()
	u.answer(ap.ID, model.AnswerAllow)
	<-ch
	if ap.SessionID != "claude:s1" || ap.AllowSession || !slices.Contains(ap.Reasons, model.ReasonTainted) {
		t.Fatalf("approval after re-creation: %+v", ap)
	}
}

// An approval card can still be open when the user deletes its session.
// Its answer is logged under the session, which comes back for it.
func TestCallAnsweredAfterSessionDeleted(t *testing.T) {
	t.Run("agent tool", func(t *testing.T) {
		h := newHarness(t, time.Minute)
		u := h.ui()
		h.hook("s1", "SessionStart", "", nil, 100)
		done := make(chan ipc.Message, 1)
		go func() { done <- h.hook("s1", "PermissionRequest", "Bash", map[string]any{"command": "ls"}, 100) }()
		ap := u.approval()
		deleted := u.deleteSession("claude:s1")
		u.answer(ap.ID, model.AnswerAllow)
		if d, _ := ipc.Decode[ipc.HookDecision](<-done); d.Behavior != ipc.BehaviorAllow {
			t.Fatalf("decision: %+v", d)
		}
		checkLoggedUnderRevived(t, h, u, model.KindAgent, deleted)
	})
	t.Run("gateway tool", func(t *testing.T) {
		h := newHarness(t, time.Minute)
		u := h.ui()
		a := h.agent("s1", 100)
		ch := a.callAsync("dev_write_sim", map[string]any{"text": "x"})
		ap := u.approval()
		deleted := u.deleteSession("claude:s1")
		u.answer(ap.ID, model.AnswerAllow)
		if res := <-ch; res.IsError {
			t.Fatalf("result: %+v", res)
		}
		checkLoggedUnderRevived(t, h, u, model.KindPlugin, deleted)
	})
}

func checkLoggedUnderRevived(t *testing.T, h *harness, u *fakeUI, kind string, deleted time.Time) {
	t.Helper()
	c := h.lastCall()
	if c.SessionID != "claude:s1" || c.Kind != kind || c.Decision != model.DecisionUserAllowed {
		t.Fatalf("log: %+v", c)
	}
	if s := u.recreated("claude:s1", deleted); s.Cwd != "/Users/me/proj-s1" {
		t.Fatalf("revived session sent to the app: %+v", s)
	}
	if _, err := h.st.Session(context.Background(), "claude:s1"); err != nil {
		t.Fatalf("session not stored again: %v", err)
	}
}

// gatedPlugin holds taint_sim until released.
type gatedPlugin struct {
	upstream.Plugin
	started, release chan struct{}
}

func (p gatedPlugin) CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	if name == "taint_sim" {
		p.started <- struct{}{}
		<-p.release
	}
	return p.Plugin.CallTool(ctx, name, args)
}

// A taint source that returns after its session was deleted still taints
// the session it brings back.
func TestTaintLandsOnDeletedSession(t *testing.T) {
	h := newHarness(t, time.Minute)
	gp := gatedPlugin{Plugin: devplugin.New(), started: make(chan struct{}), release: make(chan struct{})}
	h.core.Plugins().Set(gp)
	u := h.ui()
	a := h.agent("s1", 100)
	ch := a.callAsync("dev_taint_sim", map[string]any{"text": "x"})
	<-gp.started
	u.deleteSession("claude:s1")
	close(gp.release)
	if res := <-ch; res.IsError {
		t.Fatalf("result: %+v", res)
	}
	if c := h.lastCall(); c.SessionID != "claude:s1" || c.Tool != "taint_sim" {
		t.Fatalf("log: %+v", c)
	}
	if s, ok := h.core.sessions.Get("claude:s1"); !ok || !s.Tainted {
		t.Fatalf("revived session: %+v ok=%v", s, ok)
	}
}
