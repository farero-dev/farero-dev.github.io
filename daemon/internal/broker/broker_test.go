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

// A zero or negative timeout means the default deadline (Q29).
func TestNewDefaultsTimeout(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		if b := New(nil, d); b.timeout != DefaultTimeout {
			t.Errorf("New(nil, %v).timeout = %v, want %v", d, b.timeout, DefaultTimeout)
		}
	}
	if b := New(nil, time.Nanosecond); b.timeout != time.Nanosecond {
		t.Errorf("smallest positive timeout replaced: %v", b.timeout)
	}
	n := newRec()
	b := New(n, 0)
	b.AttachUI()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() { done <- b.Request(ctx, model.Approval{Tool: "x"}) }()
	a := <-n.requested
	if got := a.Deadline.Sub(a.CreatedAt); got != DefaultTimeout {
		t.Fatalf("deadline offset = %v, want %v", got, DefaultTimeout)
	}
	select {
	case r := <-done:
		t.Fatalf("zero timeout fired at once: %+v", r)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	<-done
}

// Every way a request ends must work without a notifier, and must tell the
// notifier (with the right reason) when there is one.
func TestRequestEndsWithAndWithoutNotifier(t *testing.T) {
	type end func(b *Broker, id string, cancel context.CancelFunc)
	cases := []struct {
		name    string
		timeout time.Duration
		end     end
		outcome Outcome
		reason  string
	}{
		{"timeout", 20 * time.Millisecond, func(*Broker, string, context.CancelFunc) {}, TimedOut, "timeout"},
		{"cancel", time.Minute, func(_ *Broker, _ string, c context.CancelFunc) { c() }, Cancelled, "cancelled"},
		{"answer", time.Minute, func(b *Broker, id string, _ context.CancelFunc) { b.Respond(id, model.AnswerAllow) }, Answered, "answered"},
	}
	for _, tc := range cases {
		for _, withNotifier := range []bool{false, true} {
			var n *rec
			var b *Broker
			if withNotifier {
				n = newRec()
				b = New(n, tc.timeout)
			} else {
				b = New(nil, tc.timeout)
			}
			b.AttachUI()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan Result, 1)
			go func() { done <- b.Request(ctx, model.Approval{Tool: "x"}) }()
			var id string
			if withNotifier {
				id = (<-n.requested).ID
			} else {
				for id == "" {
					if p := b.Pending(); len(p) > 0 {
						id = p[0].ID
					}
					time.Sleep(time.Millisecond)
				}
			}
			tc.end(b, id, cancel)
			if r := <-done; r.Outcome != tc.outcome {
				t.Errorf("%s notifier=%v: outcome %v, want %v", tc.name, withNotifier, r.Outcome, tc.outcome)
			}
			cancel()
			if withNotifier {
				n.mu.Lock()
				got := append([]string(nil), n.cancelled...)
				n.mu.Unlock()
				if len(got) != 1 || got[0] != id+":"+tc.reason {
					t.Errorf("%s: notifier saw %v, want %s:%s", tc.name, got, id, tc.reason)
				}
			}
		}
	}
}

// Answering one of several queued requests removes exactly that one.
func TestRespondRemovesOnlyThatRequest(t *testing.T) {
	n := newRec()
	b := New(n, time.Minute)
	b.AttachUI()
	for _, tool := range []string{"a", "b", "c"} {
		go b.Request(context.Background(), model.Approval{Tool: tool})
		<-n.requested
	}
	p := b.Pending()
	if len(p) != 3 {
		t.Fatalf("pending: %+v", p)
	}
	if err := b.Respond(p[1].ID, model.AnswerDeny); err != nil {
		t.Fatal(err)
	}
	left := b.Pending()
	if len(left) != 2 || left[0].ID != p[0].ID || left[1].ID != p[2].ID {
		t.Fatalf("after answering the middle one: %+v", left)
	}
	b.DetachUI()
}
