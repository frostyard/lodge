package gateway

import (
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
