// Package wsline turns a WebSocket that carries one JSON-RPC message per
// text frame into a net.Conn that carries one message per line, the
// framing the apps' Unix-socket servers read and write.
package wsline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// MaxMessage is the largest frame either side may send: 16 MiB
// (app-contract.md, /api).
const MaxMessage = 16 << 20

// Conn is a net.Conn over a WebSocket.
//
//   - Read returns each text frame in turn, followed by a newline when the
//     frame does not end with one. A binary frame closes the connection.
//   - Write sends each complete line, its newline included, as one text
//     frame, which is what the apps' frontends read today. JSON parsers
//     ignore the trailing newline. A line is buffered until its newline.
//   - A deadline that passes closes the connection.
type Conn struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc

	rmu  sync.Mutex
	r    io.Reader // the frame being read, or nil
	last byte      // the last byte of the frame being read
	owed bool      // a newline is owed after the frame just read

	wmu  sync.Mutex
	wbuf []byte

	rdl, wdl deadline
}

// New returns a Conn over ws, bounded by ctx: when ctx ends, the
// connection closes. It sets ws's read limit to MaxMessage.
func New(ctx context.Context, ws *websocket.Conn) *Conn {
	ws.SetReadLimit(MaxMessage)
	ctx, cancel := context.WithCancel(ctx)
	c := &Conn{ws: ws, ctx: ctx, cancel: cancel}
	c.rdl.close, c.wdl.close = c.expire, c.expire
	return c
}

// Read implements net.Conn.
func (c *Conn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if c.owed {
			c.owed = false
			p[0] = '\n'
			return 1, nil
		}
		if c.r == nil {
			typ, r, err := c.ws.Reader(c.ctx)
			if err != nil {
				return 0, c.readErr(err)
			}
			if typ != websocket.MessageText {
				_ = c.ws.Close(websocket.StatusUnsupportedData, "text frames only")
				return 0, errors.New("wsline: a binary frame")
			}
			c.r, c.last = r, '\n'
		}
		n, err := c.r.Read(p)
		if n > 0 {
			c.last = p[n-1]
		}
		if err == io.EOF {
			c.r = nil
			c.owed = c.last != '\n'
			err = nil
		}
		if err != nil {
			return n, c.readErr(err)
		}
		if n > 0 {
			return n, nil
		}
	}
}

func (c *Conn) readErr(err error) error {
	switch websocket.CloseStatus(err) {
	case websocket.StatusNormalClosure, websocket.StatusGoingAway, websocket.StatusNoStatusRcvd:
		return io.EOF
	}
	if c.ctx.Err() != nil {
		return net.ErrClosed
	}
	return err
}

// Write implements net.Conn.
func (c *Conn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(c.wbuf)+len(p) > MaxMessage {
				return n - len(p), fmt.Errorf("wsline: a line over %d bytes", MaxMessage)
			}
			c.wbuf = append(c.wbuf, p...)
			break
		}
		line := p[:i+1]
		if len(c.wbuf) > 0 {
			line = append(c.wbuf, line...)
			c.wbuf = nil
		}
		if err := c.ws.Write(c.ctx, websocket.MessageText, line); err != nil {
			if c.ctx.Err() != nil {
				err = net.ErrClosed
			}
			return n - len(p), err
		}
		p = p[i+1:]
	}
	return n, nil
}

// Close closes the WebSocket with a normal closure.
func (c *Conn) Close() error {
	defer c.cancel()
	return c.ws.Close(websocket.StatusNormalClosure, "")
}

// expire closes the connection at once, when a deadline passes.
func (c *Conn) expire() {
	c.cancel()
	_ = c.ws.CloseNow()
}

// LocalAddr implements net.Conn.
func (c *Conn) LocalAddr() net.Addr { return addr{} }

// RemoteAddr implements net.Conn.
func (c *Conn) RemoteAddr() net.Addr { return addr{} }

// SetDeadline implements net.Conn.
func (c *Conn) SetDeadline(t time.Time) error {
	c.rdl.set(t)
	c.wdl.set(t)
	return nil
}

// SetReadDeadline implements net.Conn.
func (c *Conn) SetReadDeadline(t time.Time) error { c.rdl.set(t); return nil }

// SetWriteDeadline implements net.Conn.
func (c *Conn) SetWriteDeadline(t time.Time) error { c.wdl.set(t); return nil }

var _ net.Conn = (*Conn)(nil)

type addr struct{}

func (addr) Network() string { return "websocket" }
func (addr) String() string  { return "lodge" }

// deadline closes the connection when its time passes.
type deadline struct {
	mu    sync.Mutex
	t     *time.Timer
	close func()
}

func (d *deadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.t != nil {
		d.t.Stop()
		d.t = nil
	}
	if !t.IsZero() {
		d.t = time.AfterFunc(time.Until(t), d.close)
	}
}
