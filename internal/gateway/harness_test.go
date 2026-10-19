package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/frostyard/lodge/internal/allow"
	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/apptest"
	"github.com/frostyard/lodge/internal/peer"
	"github.com/frostyard/lodge/internal/sessions"
	"github.com/frostyard/lodge/internal/store"
	"github.com/frostyard/lodge/internal/tokens"
)

const (
	lodgeHost   = "lodge.example"
	contentHost = "lodge-content.example"
	lodgeOrigin = "https://" + lodgeHost
)

// fakeWhoIs names every address as one configurable peer.
type fakeWhoIs struct {
	mu    sync.Mutex
	peer  peer.Peer
	err   error
	calls atomic.Int64
}

func (f *fakeWhoIs) WhoIs(_ context.Context, _ string) (peer.Peer, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peer, f.err
}

func (f *fakeWhoIs) set(fn func(p *peer.Peer)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(&f.peer)
}

func (f *fakeWhoIs) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

var loopbacks = []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1")}

func alice() peer.Peer {
	return peer.Peer{NodeID: "nPhone1CNTRL", NodeName: "phone", Addrs: loopbacks, Login: "alice@example.com", DisplayName: "Alice Zoë"}
}

// seen is what a fake app received.
type seen struct {
	Method, Path, RawPath, Query, Host string
	Header                             http.Header
}

// fakeApp records requests and answers by path.
type fakeApp struct {
	mu   sync.Mutex
	reqs []seen
	// handle, when set, answers instead of the default.
	handle func(w http.ResponseWriter, r *http.Request) bool
}

func (a *fakeApp) last(t *testing.T) seen {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.reqs) == 0 {
		t.Fatal("the app received no request")
	}
	return a.reqs[len(a.reqs)-1]
}

func (a *fakeApp) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.reqs)
}

func (a *fakeApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		apptest.Healthy("1.0")(w, r)
		return
	}
	a.mu.Lock()
	a.reqs = append(a.reqs, seen{r.Method, r.URL.Path, r.URL.RawPath, r.URL.RawQuery, r.Host, r.Header.Clone()})
	h := a.handle
	a.mu.Unlock()
	if h != nil && h(w, r) {
		return
	}
	if r.URL.Path == "/api" || (strings.HasPrefix(r.URL.Path, "/device/") && r.Header.Get("Upgrade") != "") {
		echo(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = io.WriteString(w, "app "+r.URL.Path)
}

// echo accepts a WebSocket and sends back every message.
func echo(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: r.Header.Values("Sec-Websocket-Protocol"), InsecureSkipVerify: true})
	if err != nil {
		return
	}
	c.SetReadLimit(64 << 20)
	ctx := context.Background()
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			c.Close(websocket.StatusNormalClosure, "")
			return
		}
		if err := c.Write(ctx, typ, data); err != nil {
			return
		}
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	t        *testing.T
	root     string
	g        *Gateway
	who      *fakeWhoIs
	lodge    *httptest.Server
	content  *httptest.Server
	allow    *allow.Lists
	tokens   *tokens.Store
	sessions *sessions.Registry
	apps     *apps.Registry
	clock    *clock
	notes    *fakeApp
	mail     *fakeApp
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &harness{t: t, root: root, who: &fakeWhoIs{peer: alice()}, clock: &clock{t: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)},
		notes: &fakeApp{}, mail: &fakeApp{}}
	apptest.InstallShell(t, root, map[string]string{"shell.js": "console.log(1)", "assets/app-1.js": "x"})
	apptest.Install(t, root, "notes", map[string]any{"name": "Notes", "key": "n", "content": true, "device": true, "mcp": true, "remoteImages": true},
		map[string]string{"assets/main-abc.js": "js", "style.css": "css", "data.json": "{}", "font.woff2": "w", "blob.bin": "b", "sub/page.html": "p"})
	apptest.Install(t, root, "mail", map[string]any{"name": "Mail", "key": "m", "content": true, "contentLinks": true}, nil)
	apptest.Install(t, root, "plain", map[string]any{"name": "Plain"}, nil)
	apptest.Install(t, root, "off", map[string]any{"name": "Off", "contract": 9, "content": true, "device": true, "mcp": true}, nil)
	apptest.Serve(t, root, "notes", h.notes)
	apptest.Serve(t, root, "mail", h.mail)
	h.apps = apps.New(root, nil)
	if err := h.apps.Load(); err != nil {
		t.Fatal(err)
	}
	h.apps.Poll(t.Context())
	h.allow = allow.New(db, h.clock.now)
	h.tokens = tokens.New(db, h.clock.now)
	h.sessions = sessions.NewRegistry()
	if err := h.allow.AllowLogin(t.Context(), "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := h.allow.AllowNode(t.Context(), "nPhone1CNTRL", "phone"); err != nil {
		t.Fatal(err)
	}
	h.g = New(Config{Names: TailnetNames(lodgeHost, contentHost), Root: root, Apps: h.apps, Allow: h.allow,
		Tokens: h.tokens, Sessions: h.sessions, Now: h.clock.now})
	h.lodge = start(t, h.g.Lodge(h.who))
	h.content = start(t, h.g.Content(h.who))
	return h
}

func start(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(handler)
	ts.Config = Server(handler)
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

// req builds a request to the lodge node, with LODGE as its Host.
func (h *harness) req(method, path string, hdr map[string]string) *http.Request {
	return h.reqTo(h.lodge, lodgeHost, method, path, hdr)
}

func (h *harness) reqTo(ts *httptest.Server, host, method, path string, hdr map[string]string) *http.Request {
	h.t.Helper()
	r, err := http.NewRequestWithContext(h.t.Context(), method, ts.URL+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	r.Host = host
	r.Close = true // a fresh connection, so each request asks WhoIs
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

type answer struct {
	status int
	header http.Header
	body   string
}

func (h *harness) do(r *http.Request) answer {
	h.t.Helper()
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return answer{resp.StatusCode, resp.Header, string(b)}
}

func (h *harness) get(path string, hdr map[string]string) answer {
	h.t.Helper()
	return h.do(h.req(http.MethodGet, path, hdr))
}

func expect(t *testing.T, a answer, status int, bodyPart string) {
	t.Helper()
	if a.status != status || !strings.Contains(a.body, bodyPart) {
		t.Errorf("answer %d %q, want %d containing %q", a.status, a.body, status, bodyPart)
	}
}

// wsDial opens a WebSocket to the lodge node as a browser would.
func (h *harness) wsDial(path string, hdr map[string]string) (*websocket.Conn, *http.Response, error) {
	h.t.Helper()
	header := http.Header{}
	header.Set("Origin", lodgeOrigin)
	for k, v := range hdr {
		if v == "" {
			header.Del(k)
		} else {
			header.Set(k, v)
		}
	}
	ctx, cancel := context.WithTimeout(h.t.Context(), 10*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.lodge.URL, "http")+path, &websocket.DialOptions{
		HTTPHeader: header, Host: lodgeHost, CompressionMode: websocket.CompressionDisabled})
	if c != nil {
		c.SetReadLimit(64 << 20)
		h.t.Cleanup(func() { c.CloseNow() })
	}
	return c, resp, err
}

// closeCode reads from c until it fails, and returns the close code.
func closeCode(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		_, _, err := c.Read(ctx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("the socket did not close")
			}
			return websocket.CloseStatus(err)
		}
	}
}
