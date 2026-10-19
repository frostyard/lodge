// Package rpctest runs an app's RPC server on a temporary private socket
// for tests: the transport an app's own rpctest builds on. The app's
// rpctest builds its engine and services, lets a test replace any domain
// (Options.Services, frostmail's form), builds its router, and serves it
// with Serve; tests then dial the socket, or Pipe a connection served as
// another caller.
package rpctest

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/frostyard/lodge/kit/jsonrpc"
)

// Options are what every app's rpctest options hold, with S the app's
// api.Services. An app embeds them in its own.
type Options[S any] struct {
	// Services, when set, edits the services before the router is built,
	// so a test can replace any domain with a fake.
	Services func(*S)
}

// Edit applies Services to s.
func (o Options[S]) Edit(s *S) {
	if o.Services != nil {
		o.Services(s)
	}
}

// Socket returns a fresh socket path in a short private directory, which
// is removed when the test ends: a Unix socket path holds at most 108
// bytes, more than a test's temp directory leaves.
func Socket(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rpc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "rpc.sock")
}

// Run is a server under test.
type Run struct {
	// Socket is the server's private socket.
	Socket string

	ctx  context.Context
	stop func()
}

// Serve listens on a fresh private socket (jsonrpc.Listen) and runs serve
// on it until the test ends or Stop. A serve that fails, or that does not
// return within 5 s of being stopped, fails the test.
func Serve(t testing.TB, serve func(ctx context.Context, ln net.Listener) error) *Run {
	t.Helper()
	r := &Run{Socket: Socket(t)}
	ln, err := jsonrpc.Listen(r.Socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.ctx = ctx
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ln) }()
	r.stop = sync.OnceFunc(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not stop")
		}
	})
	t.Cleanup(r.stop)
	return r
}

// Context ends when the server stops.
func (r *Run) Context() context.Context { return r.ctx }

// Stop stops the server now, as a crash between requests would. The
// test's cleanup does the same; Stop is for restarting on the same data.
func (r *Run) Stop() { r.stop() }

// Dial connects to the socket; the connection closes when the test ends.
func (r *Run) Dial(t testing.TB) net.Conn {
	t.Helper()
	nc, err := net.Dial("unix", r.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nc.Close() })
	return nc
}

// Pipe serves one in-process connection with serve, as a listener that
// authenticated it would (lodge's /api, a development gateway), and
// returns the client's end. When the test ends, the client's end closes
// and serve is waited for.
func (r *Run) Pipe(t testing.TB, serve func(ctx context.Context, nc net.Conn)) net.Conn {
	t.Helper()
	server, client := net.Pipe()
	ctx, cancel := context.WithCancel(r.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve(ctx, server)
	}()
	t.Cleanup(func() {
		_ = client.Close()
		cancel()
		<-done
	})
	return client
}
