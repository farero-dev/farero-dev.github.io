package core

import (
	"sync"

	"github.com/farero-dev/farero/daemon/internal/ipc"
)

// hub fans out events to connected UI clients. Each client has a buffered
// queue; a client that falls too far behind is dropped rather than blocking
// farerod.
type hub struct {
	mu      sync.Mutex
	clients map[*uiClient]struct{}
}

type uiClient struct {
	out  chan ipc.Message
	done chan struct{}
	once sync.Once
}

func newHub() *hub { return &hub{clients: map[*uiClient]struct{}{}} }

func (h *hub) add() *uiClient {
	c := &uiClient{out: make(chan ipc.Message, 256), done: make(chan struct{})}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *hub) remove(c *uiClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	c.once.Do(func() { close(c.done) })
}

func (h *hub) broadcast(typ string, data any) {
	b, err := ipc.Marshal(data)
	if err != nil {
		return
	}
	m := ipc.Message{Type: typ, Data: b}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.out <- m:
		default:
			// Slow client: disconnect it; the app reconnects and resyncs.
			delete(h.clients, c)
			c.once.Do(func() { close(c.done) })
		}
	}
}
