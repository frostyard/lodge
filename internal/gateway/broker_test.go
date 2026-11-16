package gateway

import (
	"bytes"
	"cmp"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeBroker echoes what /nats sends it, and counts its connections.
type fakeBroker struct {
	mu    sync.Mutex
	conns int
}

func (f *fakeBroker) ServeClient(_ context.Context, c net.Conn) {
	f.mu.Lock()
	f.conns++
	f.mu.Unlock()
	_, _ = io.Copy(c, c)
}

func (f *fakeBroker) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

// A broker that is off answers 404: /nats is lodge's route, and without
// the broker there is nothing behind it.
func TestNatsWithoutABroker(t *testing.T) {
	h := newHarness(t)
	expect(t, h.get("/nats", nil), http.StatusNotFound, "not found")
}

// /nats refuses every request but a client's WebSocket on lodge's own
// address (docs/specs/lodge-http.md, The broker).
func TestNatsRefusals(t *testing.T) {
	h := newHarness(t)
	b := &fakeBroker{}
	h.g.cfg.Broker = b
	for name, tt := range map[string]struct {
		method, host string
		hdr          map[string]string
		status       int
		body         string
	}{
		"another Host":    {method: http.MethodGet, host: "other.example", status: http.StatusForbidden, body: "this is not lodge's address"},
		"an Origin":       {method: http.MethodGet, status: http.StatusForbidden, body: "browsers may not call /nats", hdr: map[string]string{"Origin": lodgeOrigin}},
		"a browser's GET": {method: http.MethodGet, status: http.StatusBadRequest, body: "/nats takes a WebSocket"},
		"a POST":          {method: http.MethodPost, status: http.StatusMethodNotAllowed, body: "is not allowed here"},
	} {
		t.Run(name, func(t *testing.T) {
			r := h.req(tt.method, "/nats", tt.hdr)
			r.Host = cmp.Or(tt.host, lodgeHost)
			expect(t, h.do(r), tt.status, tt.body)
		})
	}
	if got := b.count(); got != 0 {
		t.Errorf("the broker got %d connections, want 0", got)
	}
}

// A peer that WhoIs cannot name is refused: /nats is on the tailnet.
func TestNatsNeedsANamedPeer(t *testing.T) {
	h := newHarness(t)
	b := &fakeBroker{}
	h.g.cfg.Broker = b
	h.who.fail(io.ErrUnexpectedEOF)
	r := h.req(http.MethodGet, "/nats", map[string]string{
		"Connection": "upgrade", "Upgrade": "websocket", "Sec-Websocket-Version": "13",
		"Sec-Websocket-Key": "dGhlIHNhbXBsZSBub25jZQ==",
	})
	expect(t, h.do(r), http.StatusForbidden, "lodge cannot name this peer")
	if got := b.count(); got != 0 {
		t.Errorf("the broker got %d connections, want 0", got)
	}
}

// A good upgrade reaches the broker, and moves bytes both ways.
func TestNatsMovesBytes(t *testing.T) {
	h := newHarness(t)
	b := &fakeBroker{}
	h.g.cfg.Broker = b

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.lodge.URL, "http")+"/nats", &websocket.DialOptions{Host: lodgeHost})
	if err != nil {
		if resp != nil {
			t.Fatalf("the upgrade was refused with %d", resp.StatusCode)
		}
		t.Fatal(err)
	}
	defer c.CloseNow()
	const line = "CONNECT {\"auth_token\":\"t\"}\r\n"
	if err := c.Write(ctx, websocket.MessageBinary, []byte(line)); err != nil {
		t.Fatal(err)
	}
	typ, got, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if typ != websocket.MessageBinary || string(got) != line {
		t.Errorf("the broker answered %q as type %d, want %q", got, typ, line)
	}
	c.Close(websocket.StatusNormalClosure, "")
	if got := b.count(); got != 1 {
		t.Errorf("the broker got %d connections, want 1", got)
	}
}

// natsURL is the dev harness's /nats address.
func natsURL(h *harness) string {
	return "ws" + strings.TrimPrefix(h.lodge.URL, "http") + "/nats"
}

// dialNats opens /nats as a client does: no Origin, lodge's own Host.
func dialNats(t *testing.T, h *harness) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, natsURL(h), &websocket.DialOptions{Host: lodgeHost})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

// capBroker answers the error its read ends with, and echoes what it reads.
type capBroker struct{ end chan error }

func (b *capBroker) ServeClient(_ context.Context, c net.Conn) {
	_, err := io.Copy(c, c)
	b.end <- err
}

// A message over the broker's 64 KiB payload cap ends the connection: the
// WebSocket's own read limit is set again after NetConn, which turns it off.
func TestNatsMessageCap(t *testing.T) {
	h := newHarness(t)
	b := &capBroker{end: make(chan error, 1)}
	h.g.cfg.Broker = b
	c := dialNats(t, h)
	c.SetReadLimit(-1)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	big := bytes.Repeat([]byte("x"), MaxBrokerMessage+1)
	_ = c.Write(ctx, websocket.MessageBinary, big)
	select {
	case err := <-b.end:
		if err == nil {
			t.Fatal("the broker read a message over its cap")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the broker's read did not end")
	}
	for {
		_, got, err := c.Read(ctx)
		if err != nil {
			t.Logf("the connection ended with %v (status %d)", err, websocket.CloseStatus(err))
			if s := websocket.CloseStatus(err); s != websocket.StatusMessageTooBig {
				t.Errorf("the connection ended with %v (status %d), want the message-too-big close", err, s)
			}
			break
		}
		if len(got) > MaxBrokerMessage {
			t.Fatalf("the broker echoed %d bytes, over its cap", len(got))
		}
	}
}

// closeBroker closes the connection it is given, as a revoke does, and says
// when the handler is done.
type closeBroker struct{ closed chan struct{} }

func (b *closeBroker) ServeClient(_ context.Context, c net.Conn) {
	_ = c.Close()
	close(b.closed)
}

// A revoke ends a WebSocket at once, with no close handshake: an
// uncooperative peer that never reads cannot hold the handler.
func TestNatsClosesAtOnce(t *testing.T) {
	h := newHarness(t)
	b := &closeBroker{closed: make(chan struct{})}
	h.g.cfg.Broker = b
	c := dialNats(t, h)
	// The client does nothing more: it never reads, so it never answers a
	// close frame.
	select {
	case <-b.closed:
	case <-time.After(time.Second):
		t.Fatal("the handler did not close the connection within 1 s")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err == nil {
		t.Error("the WebSocket is still open after the handler ended it")
	}
}
