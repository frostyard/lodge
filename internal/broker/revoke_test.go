package broker

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// A CONNECT line whose options are not an object, null included, is refused
// on both doors, and the broker keeps serving: lodge writes the one-time
// secret into the options, which a nil map has none of.
func TestConnectNullIsRefused(t *testing.T) {
	h := newHarness(t)

	// The app socket, as raw as an app daemon's connection gets.
	c, err := net.Dial("unix", h.sock)
	noErr(t, err)
	defer c.Close()
	noErr(t, c.SetDeadline(time.Now().Add(10*time.Second)))
	br := bufio.NewReader(c)
	info, err := br.ReadString('\n')
	noErr(t, err)
	if !strings.HasPrefix(info, "INFO ") {
		t.Fatalf("the first line was %q, want INFO", info)
	}
	_, err = c.Write([]byte("CONNECT null\r\n"))
	noErr(t, err)
	refusal, err := br.ReadString('\n')
	noErr(t, err)
	if got := strings.TrimSpace(refusal); got != "-ERR 'Authorization Violation'" {
		t.Errorf("the app socket answered %q, want an authorization violation", got)
	}

	// The client door.
	inside, outside := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	t.Cleanup(func() { outside.Close() })
	go h.b.ServeClient(ctx, outside)
	noErr(t, inside.SetDeadline(time.Now().Add(10*time.Second)))
	pipe := bufio.NewReader(inside)
	info, err = pipe.ReadString('\n')
	noErr(t, err)
	if !strings.HasPrefix(info, "INFO ") {
		t.Fatalf("the first line was %q, want INFO", info)
	}
	_, err = inside.Write([]byte("CONNECT null\r\n"))
	noErr(t, err)
	refusal, err = pipe.ReadString('\n')
	noErr(t, err)
	if got := strings.TrimSpace(refusal); got != "-ERR 'Authorization Violation'" {
		t.Errorf("the client door answered %q, want an authorization violation", got)
	}

	// The broker still serves the next connection.
	nc, _ := h.app(t, "marks")
	published(t, jsFor(t, nc), "marks.x", "e1")
}

// A revoke closes the client's connections at once, even when its consumers
// cannot be deleted: a deletion that hangs does not hold the door, and the
// client cannot reconnect once its token is gone.
func TestRevokeClosesFirstWhenCleanupFails(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	closed := make(chan struct{})
	nc, _ := h.clientOpts(t, "chani", "tok-chani", natsClosed(closed))
	noErr(t, nc.Flush())
	// Each deletion waits 5 s, as a server that does not answer does.
	restore := h.slowDeletes(5 * time.Second)
	defer restore()

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- h.b.Revoke(t.Context(), "chani") }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatalf("the client's connection is still open 1 s after its revoke")
	}
	t.Logf("closed after %s", time.Since(start))
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a revoke whose deletion hangs answered no error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the revoke did not finish within 2 s")
	}
	restore()
	if names := h.names(t, "app_marks"); len(names) != 1 || names[0] != "chani-s1" {
		t.Fatalf("the consumers are %q after a failed revoke, want chani-s1", names)
	}

	h.revokeToken("tok-chani")
	if nc, err := h.connectClient(t, "chani", "tok-chani"); err == nil {
		nc.Close()
		t.Fatal("a revoked client connected again")
	}
}

// slowJS delays each consumer deletion, as a server that does not answer
// does. Every other JetStream call passes through.
type slowJS struct {
	jetstream.JetStream
	delay time.Duration
}

func (s slowJS) DeleteConsumer(ctx context.Context, stream, consumer string) error {
	tick := time.NewTimer(s.delay)
	defer tick.Stop()
	select {
	case <-tick.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.JetStream.DeleteConsumer(ctx, stream, consumer)
}

// A revoke that cannot delete a consumer keeps it, and its next try deletes
// it.
func TestRevokeKeepsItsCleanup(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	dead, cancel := context.WithCancel(t.Context())
	cancel()
	if err := h.b.Revoke(dead, "chani"); err == nil {
		t.Fatal("a revoke whose deletion fails answered no error")
	}
	if names := h.names(t, "app_marks"); len(names) != 1 || names[0] != "chani-s1" {
		t.Fatalf("the consumers are %q after a failed revoke, want chani-s1", names)
	}
	// The kept consumer is in no index a connection reads.
	h.b.mu.Lock()
	kept := h.b.clients["chani"]
	pending := len(h.b.revoking["chani"])
	h.b.mu.Unlock()
	if kept != nil || pending != 1 {
		t.Errorf("the index holds %+v and %d kept consumers, want no client and 1", kept, pending)
	}

	noErr(t, h.b.Revoke(t.Context(), "chani"))
	if names := h.names(t, "app_marks"); len(names) != 0 {
		t.Errorf("the consumers are %q after the second revoke, want none", names)
	}
	if err := h.b.Revoke(t.Context(), "chani"); err != nil {
		t.Errorf("a revoke with nothing left answered %v", err)
	}
}

// A failed revoke's consumer comes back at the next start, from its own
// metadata, and a revoke then deletes it.
func TestRevokeKeepsItsCleanupAcrossARestart(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s2", "marks", []string{"marks.>"}))
	dead, cancel := context.WithCancel(t.Context())
	cancel()
	if err := h.b.Revoke(dead, "chani"); err == nil {
		t.Fatal("a revoke whose deletion fails answered no error")
	}
	h.restart(t)
	if names := h.names(t, "app_marks"); len(names) != 2 {
		t.Fatalf("the consumers are %q after a restart, want 2", names)
	}

	noErr(t, h.b.Revoke(t.Context(), "chani"))
	if names := h.names(t, "app_marks"); len(names) != 0 {
		t.Errorf("the consumers are %q after the revoke, want none", names)
	}
	h.restart(t)
	if names := h.names(t, "app_marks"); len(names) != 0 {
		t.Errorf("the consumers are %q after a second restart, want none", names)
	}
}

// A subscription stays on 1 app: the same client and subscription name
// cannot open a second consumer on another stream, and an unsubscribe
// removes the one it has.
func TestSubscribeStaysOnOneApp(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	err := h.b.Subscribe(t.Context(), "chani", "s1", "notes", []string{"notes.>"})
	if err == nil || !strings.Contains(err.Error(), "unsubscribe") {
		t.Fatalf("a second app for the same subscription answered %v, want an error naming unsubscribe", err)
	}
	if names := h.names(t, "app_notes"); len(names) != 0 {
		t.Errorf("app_notes holds the consumers %q, want none", names)
	}
	noErr(t, h.b.Unsubscribe(t.Context(), "chani", "s1"))
	if names := h.names(t, "app_marks"); len(names) != 0 {
		t.Errorf("app_marks holds the consumers %q after an unsubscribe, want none", names)
	}
}

// A subscription on the same app updates its filters, as before.
func TestSubscribeKeepsItsFilters(t *testing.T) {
	h := newHarness(t)
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.>"}))
	noErr(t, h.b.Subscribe(t.Context(), "chani", "s1", "marks", []string{"marks.a"}))
	info := h.consumer(t, "app_marks", "chani-s1")
	if got := info.Config.FilterSubjects; len(got) != 1 || got[0] != "marks.a" {
		t.Errorf("the consumer's filters are %q, want marks.a", got)
	}
}

// natsClosed is a nats.go option that answers a closed connection.
func natsClosed(closed chan struct{}) nats.Option {
	return nats.ClosedHandler(func(*nats.Conn) { close(closed) })
}
