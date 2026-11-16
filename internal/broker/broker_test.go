package broker

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// The uids the test's fake registry names, and the app the test process
// itself is.
const (
	marksUID = 1001
	notesUID = 1002
)

// fakeApps is an app registry: uids and enabled apps.
type fakeApps struct {
	byUID   map[int]string
	enabled map[string]bool
}

func (f *fakeApps) AppForUID(uid int) (string, bool) {
	id, ok := f.byUID[uid]
	if !ok || !f.enabled[id] {
		return "", false
	}
	return id, true
}

func (f *fakeApps) AppEnabled(id string) bool { return f.enabled[id] }

// harness is a broker with its socket, its streams and its apps.
type harness struct {
	t    *testing.T
	b    *Broker
	apps *fakeApps
	sock string
	dir  string

	mu     sync.Mutex
	tokens map[string]string
	// expires is when the tokens expire: zero never.
	expires time.Time
}

// newHarness starts a broker whose app stream is marks', and whose second
// app is notes.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, dir: t.TempDir(), tokens: map[string]string{}}
	h.apps = &fakeApps{
		byUID:   map[int]string{os.Getuid(): "marks", notesUID: "notes"},
		enabled: map[string]bool{"marks": true, "notes": true},
	}
	b := New(Options{StoreDir: h.dir, Apps: h.apps, DevUID: -1,
		Tokens: h.token, Log: slog.New(slog.DiscardHandler)})
	if err := b.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	h.b = b
	ln, err := Listen(filepath.Join(h.dir, "nats.sock"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() { _ = b.ServeApps(t.Context(), ln) }()
	h.sock = ln.Addr().String()
	if err := h.b.SyncApps(t.Context(), []string{"marks", "notes"}); err != nil {
		t.Fatal(err)
	}
	return h
}

// token names the client a token belongs to, and when it expires.
func (h *harness) token(_ context.Context, token string) (string, time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	name, ok := h.tokens[token]
	return name, h.expires, ok
}

// expireIn makes every token expire after d.
func (h *harness) expireIn(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expires = time.Now().Add(d)
}

// issue lets token name client.
func (h *harness) issue(token, client string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tokens[token] = client
}

// revokeToken forgets a token, as the token store does when it revokes one.
func (h *harness) revokeToken(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.tokens, token)
}

// connectClient opens one end of a pipe and hands the other to ServeClient,
// and answers the connection or its error.
func (h *harness) connectClient(t *testing.T, name, token string) (*nats.Conn, error) {
	t.Helper()
	inside, outside := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	t.Cleanup(func() { outside.Close() })
	go h.b.ServeClient(ctx, outside)
	return nats.Connect("nats://broker", nats.SetCustomDialer(&pipeDialer{c: inside}), nats.SkipHostLookup(),
		nats.Token(token), nats.CustomInboxPrefix("_INBOX."+name), nats.NoReconnect(), nats.Timeout(10*time.Second),
		nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) {}))
}

// slowDeletes makes each consumer deletion wait d, as a server that does not
// answer does, and answers what restores the real store.
func (h *harness) slowDeletes(d time.Duration) (restore func()) {
	h.b.mu.Lock()
	real := h.b.js
	h.b.js = slowJS{JetStream: real, delay: d}
	h.b.mu.Unlock()
	return func() {
		h.b.mu.Lock()
		h.b.js = real
		h.b.mu.Unlock()
	}
}

// restart closes the harness's broker, and starts a new one over its store.
func (h *harness) restart(t *testing.T) {
	t.Helper()
	h.b.Close()
	b := New(Options{StoreDir: h.dir, Apps: h.apps, DevUID: -1, Tokens: h.token, Log: slog.New(slog.DiscardHandler)})
	noErr(t, b.Start(t.Context()))
	t.Cleanup(b.Close)
	h.b = b
}

// errs collects a connection's asynchronous errors.
type errs struct {
	mu   sync.Mutex
	seen []string
	ch   chan string
}

func newErrs() *errs { return &errs{ch: make(chan string, 64)} }

func (e *errs) handler(_ *nats.Conn, _ *nats.Subscription, err error) {
	e.mu.Lock()
	e.seen = append(e.seen, err.Error())
	e.mu.Unlock()
	select {
	case e.ch <- err.Error():
	default:
	}
}

// wait waits for an error containing want.
func (e *errs) wait(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case got := <-e.ch:
			if strings.Contains(got, want) {
				return
			}
		case <-deadline:
			e.mu.Lock()
			defer e.mu.Unlock()
			t.Fatalf("no error containing %q; the errors were %q", want, e.seen)
		}
	}
}

// app connects to the broker's socket as the app its peer uid names.
func (h *harness) app(t *testing.T, user string) (*nats.Conn, *errs) {
	t.Helper()
	e := newErrs()
	nc, err := nats.Connect("nats://broker", nats.SetCustomDialer(unixDialer(h.sock)), nats.SkipHostLookup(),
		nats.NoReconnect(), nats.Timeout(10*time.Second), nats.UserInfo(user, ""), nats.ErrorHandler(e.handler))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc, e
}

// unixDialer dials a Unix socket, in place of a TCP address.
type unixDialer string

func (d unixDialer) Dial(_, _ string) (net.Conn, error) { return net.Dial("unix", string(d)) }

// pipeDialer answers one end of a pipe, which ServeClient serves.
type pipeDialer struct {
	mu sync.Mutex
	c  net.Conn
}

func (d *pipeDialer) Dial(_, _ string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.c
	d.c = nil
	if c == nil {
		return nil, errors.New("the pipe is used")
	}
	return c, nil
}

// client opens one end of a pipe and hands the other to ServeClient.
func (h *harness) client(t *testing.T, name, token string) (*nats.Conn, *errs) {
	t.Helper()
	return h.clientOpts(t, name, token)
}

// clientOpts is client with nats.go options of its own.
func (h *harness) clientOpts(t *testing.T, name, token string, opts ...nats.Option) (*nats.Conn, *errs) {
	t.Helper()
	h.issue(token, name)
	inside, outside := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	t.Cleanup(func() { outside.Close() })
	go h.b.ServeClient(ctx, outside)
	e := newErrs()
	opts = append([]nats.Option{
		nats.SetCustomDialer(&pipeDialer{c: inside}), nats.SkipHostLookup(),
		nats.Token(token), nats.CustomInboxPrefix("_INBOX." + name), nats.NoReconnect(),
		nats.Timeout(10 * time.Second), nats.ErrorHandler(e.handler),
	}, opts...)
	nc, err := nats.Connect("nats://broker", opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc, e
}

// stream answers a stream's message count.
func (h *harness) msgs(t *testing.T, stream string) uint64 {
	t.Helper()
	st, err := h.b.js.Stream(t.Context(), stream)
	if err != nil {
		t.Fatalf("stream %s: %v", stream, err)
	}
	info, err := st.Info(t.Context())
	if err != nil {
		t.Fatalf("stream %s: %v", stream, err)
	}
	return info.State.Msgs
}

// consumer answers one consumer's info, retrying while JetStream catches up.
func (h *harness) consumer(t *testing.T, stream, consumer string) *jetstream.ConsumerInfo {
	t.Helper()
	return wait(t, func() (*jetstream.ConsumerInfo, error) {
		c, err := h.b.js.Consumer(t.Context(), stream, consumer)
		if err != nil {
			return nil, err
		}
		return c.Info(t.Context())
	})
}

// names answers the names of a stream's consumers.
func (h *harness) names(t *testing.T, stream string) []string {
	t.Helper()
	st, err := h.b.js.Stream(t.Context(), stream)
	if err != nil {
		t.Fatalf("stream %s: %v", stream, err)
	}
	lister := st.ConsumerNames(t.Context())
	var out []string
	for name := range lister.Name() {
		out = append(out, name)
	}
	if err := lister.Err(); err != nil {
		t.Fatalf("consumers of %s: %v", stream, err)
	}
	return out
}

// published publishes data to subject and waits for the answer.
func published(t *testing.T, js jetstream.JetStream, subject, data string) *jetstream.PubAck {
	t.Helper()
	ack, err := js.Publish(t.Context(), subject, []byte(data))
	if err != nil {
		t.Fatalf("publish %s: %v", subject, err)
	}
	return ack
}

// wait calls f until it answers without an error, or a bounded deadline
// passes. It never sleeps: it polls a condition with a timer's ticks.
func wait[T any](t *testing.T, f func() (T, error)) T {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	var last error
	for {
		v, err := f()
		if err == nil {
			return v
		}
		last = err
		if time.Now().After(deadline) {
			t.Fatalf("the condition did not hold: %v", last)
		}
		<-tick.C
	}
}

// jsFor is a JetStream handle over nc.
func jsFor(t *testing.T, nc *nats.Conn) jetstream.JetStream {
	t.Helper()
	js, err := jetstream.New(nc)
	noErr(t, err)
	return js
}

// noErr fails a test when err is not nil.
func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
