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
	// o are the broker's options, and ids the apps its streams are for:
	// restart starts the same broker again.
	o   broker.Options
	ids []string
	// ln is the broker's app socket.
	ln net.Listener
	// app is the connection the test publishes on, as marks.
	app *nats.Conn
}

// newHarness starts a broker over a temp dir, with a stream for marks.
func newHarness(t *testing.T, opts ...func(*broker.Options)) *harness {
	t.Helper()
	h := &harness{t: t, dir: t.TempDir(), apps: newFakeApps(), auth: newFakeOAuth(), ids: []string{marksApp}}
	h.o = broker.Options{StoreDir: h.dir, Apps: h.apps, DevUID: -1, Tokens: h.tokens,
		Log: slog.New(slog.DiscardHandler)}
	for _, f := range opts {
		f(&h.o)
	}
	h.start(t)
	// A start of lodge restores the subscriptions, and admits no client
	// before it has: a test that restarts runs Restore itself.
	if err := h.svc.Restore(t.Context()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	return h
}

// start starts the harness's broker, serves its app socket, and makes the
// service over it. That service is not ready: Restore, which a start of lodge
// runs before it serves, admits no client until it has finished.
func (h *harness) start(t *testing.T) {
	t.Helper()
	h.b = broker.New(h.o)
	if err := h.b.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.b.Close)
	h.listen(t)
	if err := h.b.SyncApps(t.Context(), h.ids); err != nil {
		t.Fatal(err)
	}
	h.svc = events.New(events.Options{Broker: h.b, OAuth: h.auth, Apps: h.apps,
		Log: slog.New(slog.DiscardHandler)})
	h.app = h.connectApp(t)
}

// listen makes the broker's app socket, and serves it with the harness's
// broker.
func (h *harness) listen(t *testing.T) {
	t.Helper()
	ln, err := broker.Listen(filepath.Join(h.dir, "nats.sock"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	b := h.b
	go func() { _ = b.ServeApps(t.Context(), ln) }()
	h.ln = ln
	h.sock = ln.Addr().String()
}

// restart closes the broker, and starts a new 1 over the same store and
// socket, as a start of lodge does. Its service is not ready: the test runs
// Restore, and the door admits no client before it.
func (h *harness) restart(t *testing.T) *events.Service {
	t.Helper()
	h.b.Close()
	h.ln.Close()
	h.app.Close()
	h.start(t)
	return h.svc
}

// tokens is the broker's Options.Tokens: the service's own answer.
func (h *harness) tokens(ctx context.Context, token string) (string, time.Time, bool) {
	return h.svc.Tokens(ctx, token)
}

// addApp gives the harness another enabled app, with 1 event on its group
// selector, and its stream.
func (h *harness) addApp(t *testing.T, id string) {
	t.Helper()
	h.apps.enabled[id] = true
	h.apps.hearth[id] = &apps.Hearth{Capabilities: []string{id + ":read"}, Selectors: []string{"group"},
		Events: []apps.Event{{Type: "org.frostyard." + id + ".note.created", Message: id + ".v1.NoteCreated",
			Topic: id + ".note.created.group", Selector: "group", ReadPermission: id + ":read"}}}
	h.ids = append(h.ids, id)
	if err := h.b.SyncApps(t.Context(), h.ids); err != nil {
		t.Fatal(err)
	}
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
	// armed is 1 grant read that stops after it has taken the grant, until
	// its release is closed: 1 test narrows the grant in that window.
	armed   bool
	read    chan struct{}
	release chan struct{}
	// blockLookup makes the next client read wait, until its release.
	blockLookup bool
	// failing and grantErr make the next grant reads fail, failApp and
	// failAppN the next reads of 1 app, and lookups the next client reads.
	failing   int
	grantErr  error
	failApp   string
	failAppN  int
	lookups   int
	lookupErr error
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
	if f.failing > 0 {
		f.failing--
		err := f.grantErr
		f.mu.Unlock()
		return nil, err
	}
	if f.failAppN > 0 && f.failApp == app {
		f.failAppN--
		err := f.grantErr
		f.mu.Unlock()
		return nil, err
	}
	items := slices.Clone(f.grants[principal+"\x00"+app])
	if items == nil {
		items = []oauth.Item{}
	}
	armed, read, release := f.armed, f.read, f.release
	f.armed = false
	f.mu.Unlock()
	// The grant is read: the read below waits until the test narrowed it.
	if armed {
		close(read)
		<-release
	}
	return items, nil
}

// blockNextGrant makes the next grant read signal read after it took the
// grant, and wait for the answer's release.
func (f *fakeOAuth) blockNextGrant() (read, release chan struct{}) {
	read, release = make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	defer f.mu.Unlock()
	f.armed, f.read, f.release = true, read, release
	return read, release
}

// failClients makes the next n client reads fail with err.
func (f *fakeOAuth) failClients(n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups, f.lookupErr = n, err
}

// failGrantsForApp makes the next n grant reads of 1 app fail with err.
func (f *fakeOAuth) failGrantsForApp(app string, n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failApp, f.failAppN, f.grantErr = app, n, err
}

// failGrants makes the next n grant reads fail with err.
func (f *fakeOAuth) failGrants(n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing, f.grantErr = n, err
}

func (f *fakeOAuth) Client(ctx context.Context, id string) (oauth.Client, error) {
	f.mu.Lock()
	if f.lookups > 0 {
		f.lookups--
		err := f.lookupErr
		f.mu.Unlock()
		return oauth.Client{}, err
	}
	c, ok := f.clients[id]
	blocked, read, release := f.blockLookup, f.read, f.release
	f.blockLookup = false
	f.mu.Unlock()
	// The read has begun, and waits: a test cancels the start in this window,
	// and the read then fails as a store's read does with its context.
	if blocked {
		close(read)
		<-release
		if err := ctx.Err(); err != nil {
			return oauth.Client{}, err
		}
	}
	if !ok || !c.Revoked.IsZero() {
		return oauth.Client{}, oauth.ErrNotFound
	}
	return c, nil
}

// blockNextClient makes the next read of a client signal read before it
// answers, and wait for the answer's release.
func (f *fakeOAuth) blockNextClient() (read, release chan struct{}) {
	read, release = make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blockLookup, f.read, f.release = true, read, release
	return read, release
}
