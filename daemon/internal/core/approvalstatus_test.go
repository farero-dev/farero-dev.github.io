package core

import (
	"strings"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/model"
)

func (h *harness) waitStatus(id, want string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.status(id) != want {
		if time.Now().After(deadline) {
			h.t.Fatalf("status %s, want %s", h.status(id), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Claude Code 2.1.293 moves a gateway call that is still waiting after
// 120 s to the background: PostToolUse fires, the turn ends (Stop), and the
// result arrives later as a task notification. The card is still open, so
// the session keeps showing waiting_approval; once answered it shows what
// the hooks said (the turn has ended).
func TestOpenCardOutlivesBackgroundMove(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	a := h.agent("s1", 100)
	ch := a.callAsync("dev_write_sim", map[string]any{"text": "late"})
	ap := u.approval()
	h.waitStatus("claude:s1", model.StatusWaitingApproval)
	h.hook("s1", "PostToolUse", GatewayPrefix+"dev_write_sim", map[string]any{"text": "late"}, 100)
	h.hook("s1", "Stop", "", nil, 100)
	if got := h.status("claude:s1"); got != model.StatusWaitingApproval {
		t.Fatalf("status with the card open: %s", got)
	}
	u.answer(ap.ID, model.AnswerAllow)
	if res := <-ch; res.IsError {
		t.Fatalf("result: %+v", res)
	}
	h.waitStatus("claude:s1", model.StatusWaitingInput)
}

// The user can type a new prompt while a gateway card is open (no terminal
// prompt blocks the input).
func TestOpenCardOutlivesNewPrompt(t *testing.T) {
	h := newHarness(t, time.Minute)
	u := h.ui()
	a := h.agent("s1", 100)
	h.hook("s1", "Stop", "", nil, 100)
	ch := a.callAsync("dev_write_sim", map[string]any{"text": "x"})
	ap := u.approval()
	h.hook("s1", "UserPromptSubmit", "", nil, 100)
	if got := h.status("claude:s1"); got != model.StatusWaitingApproval {
		t.Fatalf("status with the card open: %s", got)
	}
	u.answer(ap.ID, model.AnswerAllow)
	<-ch
	h.waitStatus("claude:s1", model.StatusRunning)
}

// A card that times out after the turn ended must not flip the idle session
// back to running.
func TestTimedOutCardLeavesIdleSession(t *testing.T) {
	h := newHarness(t, 200*time.Millisecond)
	u := h.ui()
	a := h.agent("s1", 100)
	ch := a.callAsync("dev_write_sim", map[string]any{"text": "x"})
	u.approval()
	h.hook("s1", "PostToolUse", GatewayPrefix+"dev_write_sim", map[string]any{"text": "x"}, 100)
	h.hook("s1", "Stop", "", nil, 100)
	if res := <-ch; !res.IsError || !strings.Contains(text(res), "시간 초과") {
		t.Fatalf("result: %+v", res)
	}
	h.waitStatus("claude:s1", model.StatusWaitingInput)
	time.Sleep(50 * time.Millisecond)
	if got := h.status("claude:s1"); got != model.StatusWaitingInput {
		t.Fatalf("status after the timeout: %s", got)
	}
}
