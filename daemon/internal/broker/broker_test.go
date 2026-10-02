package broker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/farero-dev/farero/daemon/internal/model"
)

type rec struct {
	mu        sync.Mutex
	requested chan model.Approval
	cancelled []string
}

func newRec() *rec { return &rec{requested: make(chan model.Approval, 10)} }

func (r *rec) ApprovalRequested(a model.Approval) { r.requested <- a }
func (r *rec) ApprovalCancelled(id, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelled = append(r.cancelled, id+":"+reason)
}

func TestNoUIReturnsImmediately(t *testing.T) {
	b := New(nil, time.Minute)
	if r := b.Request(context.Background(), model.Approval{Tool: "x"}); r.Outcome != UIGone {
		t.Fatalf("got %+v", r)
	}
}

func TestAnswerAndOrder(t *testing.T) {
	n := newRec()
	b := New(n, time.Minute)
	b.AttachUI()
	results := make(chan Result, 2)
	go func() { results <- b.Request(context.Background(), model.Approval{Tool: "first", AllowSession: true}) }()
	a1 := <-n.requested
	go func() { results <- b.Request(context.Background(), model.Approval{Tool: "second"}) }()
	a2 := <-n.requested
	if p := b.Pending(); len(p) != 2 || p[0].Tool != "first" || p[1].Tool != "second" {
		t.Fatalf("order: %+v", p)
	}
	if a1.Deadline.Sub(a1.CreatedAt) != time.Minute {
		t.Fatalf("deadline not set: %+v", a1)
	}
	if err := b.Respond(a2.ID, model.AnswerAllowSession); err == nil {
		t.Fatal("allow_session must be rejected when the card does not offer it")
	}
	if err := b.Respond(a1.ID, model.AnswerAllowSession); err != nil {
		t.Fatal(err)
	}
	if r := <-results; r.Outcome != Answered || r.Answer != model.AnswerAllowSession {
		t.Fatalf("got %+v", r)
	}
	n.mu.Lock()
	if len(n.cancelled) != 1 || n.cancelled[0] != a1.ID+":answered" {
		t.Fatalf("answer must be broadcast as cancelled/answered: %v", n.cancelled)
	}
	n.mu.Unlock()
	b.Respond(a2.ID, model.AnswerDeny)
	if r := <-results; r.Answer != model.AnswerDeny {
		t.Fatalf("got %+v", r)
	}
	if err := b.Respond(a2.ID, model.AnswerAllow); err != ErrUnknownApproval {
		t.Fatalf("double answer: %v", err)
	}
}

func TestTimeout(t *testing.T) {
	n := newRec()
	b := New(n, 30*time.Millisecond)
	b.AttachUI()
	r := b.Request(context.Background(), model.Approval{Tool: "x"})
	if r.Outcome != TimedOut {
		t.Fatalf("got %+v", r)
	}
	if len(b.Pending()) != 0 || len(n.cancelled) != 1 {
		t.Fatalf("pending=%v cancelled=%v", b.Pending(), n.cancelled)
	}
}

func TestCancelAndUIGone(t *testing.T) {
	n := newRec()
	b := New(n, time.Minute)
	b.AttachUI()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Result, 1)
	go func() { done <- b.Request(ctx, model.Approval{Tool: "x"}) }()
	<-n.requested
	cancel()
	if r := <-done; r.Outcome != Cancelled {
		t.Fatalf("got %+v", r)
	}

	b.AttachUI() // two UI clients
	go func() { done <- b.Request(context.Background(), model.Approval{Tool: "y"}) }()
	<-n.requested
	b.DetachUI()
	select {
	case r := <-done:
		t.Fatalf("resolved while a UI is still attached: %+v", r)
	case <-time.After(20 * time.Millisecond):
	}
	b.DetachUI()
	if r := <-done; r.Outcome != UIGone {
		t.Fatalf("got %+v", r)
	}
	if b.UIConnected() {
		t.Fatal("UI count should be zero")
	}
}
