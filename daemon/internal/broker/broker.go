// Package broker queues approval requests for the UI and waits for answers
// (기능 명세서 5-3).
//
// Requests are kept in arrival order. Each has a 10 minute deadline (Q29).
// When the last UI client disconnects every pending request is resolved as
// "UI gone" so callers can fall back (plugin calls are auto-denied, agent
// tools go back to the agent's own prompt).
package broker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/farero-dev/farero/daemon/internal/model"
)

// DefaultTimeout is the approval deadline (Q29).
const DefaultTimeout = 10 * time.Minute

// Outcome of a request.
type Outcome int

const (
	Answered  Outcome = iota // Answer holds the user's choice
	TimedOut                 // deadline passed
	UIGone                   // no UI was connected, or it disconnected while waiting
	Cancelled                // the caller's context ended (agent withdrew the request)
)

// Result is what Request returns.
type Result struct {
	Outcome Outcome
	Answer  string // model.AnswerAllow, AnswerAllowSession or AnswerDeny
}

// Notifier receives queue changes to forward to UI clients.
type Notifier interface {
	ApprovalRequested(a model.Approval)
	ApprovalCancelled(id, reason string)
}

type pending struct {
	a  model.Approval
	ch chan Result
}

// Broker is the approval queue.
type Broker struct {
	mu      sync.Mutex
	timeout time.Duration
	now     func() time.Time
	notify  Notifier
	uiCount int
	pending map[string]*pending
	order   []string
}

// New creates a broker. notify may be nil.
func New(notify Notifier, timeout time.Duration) *Broker {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Broker{timeout: timeout, now: time.Now, notify: notify, pending: map[string]*pending{}}
}

// SetNotifier sets the notifier (used when wiring components).
func (b *Broker) SetNotifier(n Notifier) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.notify = n
}

// UIConnected reports whether at least one UI client is attached.
func (b *Broker) UIConnected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.uiCount > 0
}

// AttachUI records a UI client connecting.
func (b *Broker) AttachUI() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.uiCount++
}

// DetachUI records a UI client leaving. When none remain, pending requests
// resolve as UIGone.
func (b *Broker) DetachUI() {
	b.mu.Lock()
	b.uiCount--
	if b.uiCount > 0 {
		b.mu.Unlock()
		return
	}
	b.uiCount = 0
	drained := b.drainLocked()
	b.mu.Unlock()
	for _, p := range drained {
		p.ch <- Result{Outcome: UIGone}
	}
}

func (b *Broker) drainLocked() []*pending {
	var out []*pending
	for _, id := range b.order {
		out = append(out, b.pending[id])
	}
	b.pending = map[string]*pending{}
	b.order = nil
	return out
}

// Pending returns queued requests in arrival order.
func (b *Broker) Pending() []model.Approval {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]model.Approval, 0, len(b.order))
	for _, id := range b.order {
		out = append(out, b.pending[id].a)
	}
	return out
}

// Request queues a and blocks until it is answered, times out, the UI goes
// away or ctx ends. The approval's ID, CreatedAt and Deadline are filled in.
func (b *Broker) Request(ctx context.Context, a model.Approval) Result {
	b.mu.Lock()
	if b.uiCount == 0 {
		b.mu.Unlock()
		return Result{Outcome: UIGone}
	}
	a.ID = newID()
	a.CreatedAt = b.now()
	a.Deadline = a.CreatedAt.Add(b.timeout)
	p := &pending{a: a, ch: make(chan Result, 1)}
	b.pending[a.ID] = p
	b.order = append(b.order, a.ID)
	notify := b.notify
	b.mu.Unlock()

	if notify != nil {
		notify.ApprovalRequested(a)
	}
	timer := time.NewTimer(b.timeout)
	defer timer.Stop()
	select {
	case r := <-p.ch:
		return r
	case <-timer.C:
		if b.remove(a.ID) {
			if notify != nil {
				notify.ApprovalCancelled(a.ID, "timeout")
			}
			return Result{Outcome: TimedOut}
		}
	case <-ctx.Done():
		if b.remove(a.ID) {
			if notify != nil {
				notify.ApprovalCancelled(a.ID, "cancelled")
			}
			return Result{Outcome: Cancelled}
		}
	}
	// Lost the race with Respond or DetachUI: their result is in the channel.
	return <-p.ch
}

// ErrUnknownApproval is returned for an answer to a request that is gone.
var ErrUnknownApproval = errors.New("approval not pending")

// Respond delivers the user's answer.
func (b *Broker) Respond(id, answer string) error {
	switch answer {
	case model.AnswerAllow, model.AnswerAllowSession, model.AnswerDeny:
	default:
		return errors.New("bad answer " + answer)
	}
	b.mu.Lock()
	p, ok := b.pending[id]
	if ok && answer == model.AnswerAllowSession && !p.a.AllowSession {
		b.mu.Unlock()
		return errors.New("this request cannot be allowed for the session")
	}
	if ok {
		b.removeLocked(id)
	}
	b.mu.Unlock()
	if !ok {
		return ErrUnknownApproval
	}
	p.ch <- Result{Outcome: Answered, Answer: answer}
	return nil
}

func (b *Broker) remove(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.pending[id]; !ok {
		return false
	}
	b.removeLocked(id)
	return true
}

func (b *Broker) removeLocked(id string) {
	delete(b.pending, id)
	for i, x := range b.order {
		if x == id {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
}

func newID() string {
	var buf [8]byte
	rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
