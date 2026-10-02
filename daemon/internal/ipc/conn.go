package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// MaxLine bounds one message. Hook input can carry whole file contents
// (Write tool), so this is generous.
const MaxLine = 32 << 20

// Conn reads and writes Messages on a stream.
type Conn struct {
	c  net.Conn
	r  *bufio.Reader
	mu sync.Mutex // serializes writes
}

// NewConn wraps a network connection.
func NewConn(c net.Conn) *Conn {
	return &Conn{c: c, r: bufio.NewReaderSize(c, 64<<10)}
}

// Read returns the next message.
func (c *Conn) Read() (Message, error) {
	var line []byte
	for {
		chunk, isPrefix, err := c.r.ReadLine()
		if err != nil {
			return Message{}, err
		}
		line = append(line, chunk...)
		if len(line) > MaxLine {
			return Message{}, errors.New("ipc: message too large")
		}
		if !isPrefix {
			break
		}
	}
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return Message{}, fmt.Errorf("ipc: bad message: %w", err)
	}
	return m, nil
}

// Write sends a message.
func (c *Conn) Write(m Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.c.Write(b)
	return err
}

// Send marshals data into a message of the given type.
func (c *Conn) Send(typ, id string, data any) error {
	m := Message{Type: typ, ID: id}
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		m.Data = b
	}
	return c.Write(m)
}

// SendError replies with an error message.
func (c *Conn) SendError(id string, err error) error {
	return c.Send(TypeError, id, ErrorData{Message: err.Error()})
}

// SetDeadline sets the underlying read/write deadline.
func (c *Conn) SetDeadline(t time.Time) error { return c.c.SetDeadline(t) }

// Close closes the connection.
func (c *Conn) Close() error { return c.c.Close() }

// Decode unmarshals a message's data.
func Decode[T any](m Message) (T, error) {
	var v T
	if len(m.Data) == 0 {
		return v, nil
	}
	err := json.Unmarshal(m.Data, &v)
	return v, err
}

// Dial connects to farerod with a short timeout.
func Dial(path string, timeout time.Duration) (*Conn, error) {
	c, err := net.DialTimeout("unix", path, timeout)
	if err != nil {
		return nil, err
	}
	return NewConn(c), nil
}

// Handler serves one accepted connection. first is the connection's first
// message, which tells the client kind apart.
type Handler func(ctx context.Context, c *Conn, first Message)

// Listen opens the farerod socket with mode 0600. A stale socket file is
// removed, but if another farerod answers on it Listen fails.
func Listen(path string) (net.Listener, error) {
	if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		c.Close()
		return nil, fmt.Errorf("another farerod is already listening on %s", path)
	}
	_ = os.Remove(path)
	old := umask(0o177)
	l, err := net.Listen("unix", path)
	umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// Serve accepts connections until ctx ends, handing each to h.
func Serve(ctx context.Context, l net.Listener, h Handler) error {
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		nc, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go func() {
			c := NewConn(nc)
			defer c.Close()
			// The first message must arrive promptly.
			c.SetDeadline(time.Now().Add(5 * time.Second))
			first, err := c.Read()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					c.SendError("", err)
				}
				return
			}
			c.SetDeadline(time.Time{})
			h(ctx, c, first)
		}()
	}
}
