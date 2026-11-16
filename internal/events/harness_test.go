package events_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/broker"
	"github.com/frostyard/lodge/internal/events"
	"github.com/frostyard/lodge/internal/oauth"
)

// The app the test process itself is, and the app it publishes as.
const marksApp = "marks"

// appEvents are the events of the harness's app marks: 2 with a folder
// selector, whose topics are apart, and 1 without a selector.
var appEvents = []apps.Event{
	{Type: "org.frostyard.marks.bookmark.created", Message: "marks.v1.BookmarkCreated",
		Topic: "marks.bookmark.created.folder", Selector: "folder", ReadPermission: "marks:read"},
	{Type: "org.frostyard.marks.bookmark.updated", Message: "marks.v1.BookmarkUpdated",
		Topic: "marks.bookmark.updated.folder", Selector: "folder", ReadPermission: "marks:read"},
	{Type: "org.frostyard.marks.search.done", Message: "marks.v1.SearchDone",
		Topic: "marks.search.done", ReadPermission: "marks:read"},
}

// harness is a real broker, the service over it, a fake OAuth server and a
// fake app registry.
type harness struct {
	t    *testing.T
	dir  string
	b    *broker.Broker
	svc  *events.Service
	apps *fakeApps
	auth *fakeOAuth
	sock string
	// app is the connection the test publishes on, as marks.
	app *nats.Conn
}

// newHarness starts a broker over a temp dir, with a stream for marks.
func newHarness(t *testing.T, opts ...func(*broker.Options)) *harness {
	t.Helper()
	h := &harness{t: t, dir: t.TempDir(), apps: newFakeApps(), auth: newFakeOAuth()}
	o := broker.Options{StoreDir: h.dir, Apps: h.apps, DevUID: -1, Tokens: h.tokens,
		Log: slog.New(slog.DiscardHandler)}
	for _, f := range opts {
		f(&o)
	}
	h.b = broker.New(o)
	if err := h.b.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.b.Close)
	ln, err := broker.Listen(filepath.Join(h.dir, "nats.sock"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() { _ = h.b.ServeApps(t.Context(), ln) }()
	h.sock = ln.Addr().String()
	if err := h.b.SyncApps(t.Context(), []string{marksApp}); err != nil {
		t.Fatal(err)
	}
	h.svc = events.New(events.Options{Broker: h.b, OAuth: h.auth, Apps: h.apps,
		Log: slog.New(slog.DiscardHandler)})
	h.app = h.connectApp(t)
	return h
}

// tokens is the broker's Options.Tokens: the service's own answer.
func (h *harness) tokens(ctx context.Context, token string) (string, time.Time, bool) {
	return h.svc.Tokens(ctx, token)
}

// connectApp connects to the broker's socket as marks, the app the test
// process is.
func (h *harness) connectApp(t *testing.T) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect("nats://broker", nats.SetCustomDialer(unixDialer(h.sock)), nats.SkipHostLookup(),
		nats.UserInfo(marksApp, ""), nats.NoReconnect(), nats.Timeout(10*time.Second))
	if err != nil {
		t.Fatalf("connect as the app: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// publish publishes 1 event, as the app does.
func (h *harness) publish(t *testing.T, subject string) {
	t.Helper()
	js, err := jetstream.New(h.app)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.Publish(t.Context(), subject, []byte(subject)); err != nil {
		t.Fatalf("publish %s: %v", subject, err)
	}
}

// access is what a token gives, as the service reads it.
func (h *harness) access(t *testing.T, token string) oauth.Access {
	t.Helper()
	access, err := h.auth.Check(t.Context(), token, oauth.ScopeEvents)
	if err != nil {
		t.Fatalf("check the token: %v", err)
	}
	return access
}

// connect opens 1 client connection through the broker's client door, as an
// agent's event bridge does, and answers its error.
func (h *harness) connect(t *testing.T, token string) (*nats.Conn, error) {
	t.Helper()
	name, _, ok := h.svc.Tokens(t.Context(), token)
	if !ok {
		// A token the service refuses: the connection is refused too, and
		// the inbox names no client.
		name = "refused"
	}
	inside, outside := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	t.Cleanup(func() { outside.Close() })
	go h.b.ServeClient(ctx, outside)
	nc, err := nats.Connect("nats://broker", nats.SetCustomDialer(&pipeDialer{c: inside}), nats.SkipHostLookup(),
		nats.Token(token), nats.CustomInboxPrefix("_INBOX."+name), nats.NoReconnect(), nats.Timeout(10*time.Second),
		nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) {}))
	if err != nil {
		return nil, err
	}
	t.Cleanup(nc.Close)
	return nc, nil
}

// subscribed connects as the token's client and fails the test when the
// broker refuses it.
func (h *harness) subscribed(t *testing.T, token string) *nats.Conn {
	t.Helper()
	nc, err := h.connect(t, token)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return nc
}

// pull asks 1 consumer for a batch, and answers what it gets within the
// wait: a pull that gets nothing answers an empty list.
func pull(t *testing.T, nc *nats.Conn, stream, consumer string, batch int, wait time.Duration) []*nats.Msg {
	t.Helper()
	sub, err := nc.SubscribeSync(nc.NewRespInbox())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := sub.AutoUnsubscribe(1 + batch); err != nil {
		t.Fatal(err)
	}
	req := `{"batch":` + strconv.Itoa(batch) + `,"expires":` + strconv.Itoa(int(wait.Milliseconds())) + "000000}"
	if err := nc.PublishRequest(nextPrefix+stream+"."+consumer, sub.Subject, []byte(req)); err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	var out []*nats.Msg
	deadline := time.Now().Add(wait + 5*time.Second)
	for len(out) < batch {
		m, err := sub.NextMsg(time.Until(deadline))
		if err != nil {
			break
		}
		if m.Header.Get("Status") != "" {
			// The server's own answer: the batch's end, or a timeout.
			break
		}
		out = append(out, m)
	}
	return out
}

// nextPrefix is the subject of a consumer's pull requests.
const nextPrefix = "$JS.API.CONSUMER.MSG.NEXT."

// wait polls f until it answers without an error, or a deadline passes.
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

// errNotYet is a condition that does not hold yet.
var errNotYet = errors.New("not yet")

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

// fakeApps is an app registry: uids, enabled apps and their hearth
// manifests.
type fakeApps struct {
	byUID   map[int]string
	enabled map[string]bool
	hearth  map[string]*apps.Hearth
}

func newFakeApps() *fakeApps {
	return &fakeApps{
		byUID:   map[int]string{os.Getuid(): marksApp},
		enabled: map[string]bool{marksApp: true},
		hearth: map[string]*apps.Hearth{marksApp: {Capabilities: []string{"marks:read", "marks:write"},
			Selectors: []string{"folder"}, Events: appEvents}},
	}
}

func (f *fakeApps) AppForUID(uid int) (string, bool) {
	id, ok := f.byUID[uid]
	if !ok || !f.enabled[id] {
		return "", false
	}
	return id, true
}

func (f *fakeApps) AppEnabled(id string) bool { return f.enabled[id] }

func (f *fakeApps) Get(id string) (apps.App, bool) {
	if !f.enabled[id] {
		return apps.App{}, false
	}
	a := apps.App{ID: id, Hearth: f.hearth[id]}
	return a, true
}

// fakeOAuth is lodge's OAuth server, for the service and the broker's door.
type fakeOAuth struct {
	mu      sync.Mutex
	tokens  map[string]string
	clients map[string]oauth.Client
	grants  map[string][]oauth.Item
}

func newFakeOAuth() *fakeOAuth {
	return &fakeOAuth{tokens: map[string]string{}, clients: map[string]oauth.Client{},
		grants: map[string][]oauth.Item{}}
}

// newClient adds a live client of principal with the scopes, and answers its
// id and 1 access token.
func (f *fakeOAuth) newClient(t *testing.T, principal, scopes string) (string, string) {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	id := "lgc_" + base64.RawURLEncoding.EncodeToString(raw[:])
	token := "tok-" + id
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clients[id] = oauth.Client{ID: id, Principal: principal, Scope: scopes}
	f.tokens[token] = id
	return id, token
}

// grant sets principal's grant for app.
func (f *fakeOAuth) grant(principal, app string, items []oauth.Item) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants[principal+"\x00"+app] = items
}

// setScope replaces 1 client's scope, as OAuth's store does.
func (f *fakeOAuth) setScope(id, scope string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.clients[id]
	c.Scope = scope
	f.clients[id] = c
}

// revokeClient revokes 1 client, as oauth.RevokeClient does.
func (f *fakeOAuth) revokeClient(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.clients[id]
	c.Revoked = time.Now()
	f.clients[id] = c
	for token, owner := range f.tokens {
		if owner == id {
			delete(f.tokens, token)
		}
	}
}

func (f *fakeOAuth) Check(_ context.Context, token, scope string) (oauth.Access, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.tokens[token]
	if !ok {
		return oauth.Access{}, oauth.ErrNotFound
	}
	c, ok := f.clients[id]
	if !ok || !c.Bound() {
		return oauth.Access{}, oauth.ErrNotFound
	}
	if !slices.Contains(strings.Fields(c.Scope), scope) {
		return oauth.Access{}, oauth.ErrScope
	}
	return oauth.Access{Principal: c.Principal, Client: id, Scopes: strings.Fields(c.Scope),
		Expires: time.Now().Add(time.Hour)}, nil
}

func (f *fakeOAuth) Grant(_ context.Context, principal, app string) ([]oauth.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	items, ok := f.grants[principal+"\x00"+app]
	if !ok {
		return []oauth.Item{}, nil
	}
	return slices.Clone(items), nil
}

func (f *fakeOAuth) Client(_ context.Context, id string) (oauth.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.clients[id]
	if !ok || !c.Revoked.IsZero() {
		return oauth.Client{}, oauth.ErrNotFound
	}
	return c, nil
}
