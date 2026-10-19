package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
)

// EventBuffer is how many undelivered events a Client holds before it
// fails the connection with ErrEventsOverflow; the caller then reconnects
// and resubscribes with its last seq.
const EventBuffer = 1024

// ErrEventsOverflow means the caller stopped reading Client.Notifications.
var ErrEventsOverflow = errors.New("jsonrpc: event buffer overflow")

// Client is a connection to a Server, with error codes of type C. It is
// safe for concurrent use; calls are multiplexed by request ID. An app's
// api package embeds it in its own Client, on which the generated domain
// clients hang.
type Client[C Code] struct {
	conn net.Conn

	wmu sync.Mutex // serializes writes

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan Frame[C]
	err     error

	events chan EventEnvelope
	done   chan struct{}
}

// NewClient starts a client on an established connection. The caller makes
// the server's hello first, if it has one.
func NewClient[C Code](conn net.Conn) *Client[C] {
	c := &Client[C]{
		conn:    conn,
		pending: map[int64]chan Frame[C]{},
		events:  make(chan EventEnvelope, EventBuffer),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Notifications delivers server events. It is closed when the connection
// ends; Err then says why.
func (c *Client[C]) Notifications() <-chan EventEnvelope { return c.events }

// Done is closed when the connection ends.
func (c *Client[C]) Done() <-chan struct{} { return c.done }

// Err is why the connection ended, or nil while it is open.
func (c *Client[C]) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the connection and fails pending calls.
func (c *Client[C]) Close() error {
	err := c.conn.Close()
	<-c.done
	return err
}

// Call sends a request and decodes the result into result (unless nil). A
// server error is returned as *Error[C].
func (c *Client[C]) Call(ctx context.Context, method string, params, result any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode %s params: %w", method, err)
	}
	ch := make(chan Frame[C], 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	req := Frame[C]{JSONRPC: "2.0", ID: jsontext.Value(strconv.FormatInt(id, 10)), Method: method, Params: raw}
	if err := c.write(req); err != nil {
		return err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(m.Result, result); err != nil {
			return fmt.Errorf("decode %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.Err()
	}
}

func (c *Client[C]) write(m Frame[C]) error {
	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write to the server: %w", err)
	}
	return nil
}

func (c *Client[C]) readLoop() {
	err := c.read()
	c.mu.Lock()
	if err == nil {
		err = errors.New("jsonrpc: connection closed")
	}
	c.err = err
	c.mu.Unlock()
	_ = c.conn.Close()
	close(c.events)
	close(c.done)
}

func (c *Client[C]) read() error {
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 64<<10), MaxMessageSize)
	for sc.Scan() {
		var m Frame[C]
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			return fmt.Errorf("jsonrpc: malformed message from the server: %w", err)
		}
		if len(m.ID) == 0 {
			if m.Method != EventMethod {
				continue // unknown notifications are ignored for forward compatibility
			}
			var ev EventEnvelope
			if err := json.Unmarshal(m.Params, &ev); err != nil {
				return fmt.Errorf("jsonrpc: malformed event: %w", err)
			}
			select {
			case c.events <- ev:
			default:
				return ErrEventsOverflow
			}
			continue
		}
		id, err := strconv.ParseInt(string(m.ID), 10, 64)
		if err != nil {
			continue // not one of ours
		}
		c.mu.Lock()
		ch := c.pending[id]
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
	return sc.Err()
}
