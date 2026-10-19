// Package kittest plays lodge's side of an app socket in tests: it serves
// a lodgeapp.Server on a socket in a temp root, sends requests with the
// Lodge-* headers lodge would set, and checks the contract's socket rules
// (CheckRules).
package kittest

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/frostyard/lodge/kit/internal/wsline"
	"github.com/frostyard/lodge/kit/lodgeapp"
)

// The development build's fixed identity (lodge-http.md, The development
// build), which kittest's callers carry.
const (
	Login       = "e2e@example.com"
	DisplayName = "E2E"
	NodeName    = "e2e"
	NodeID      = "e2e-node"
	SessionID   = "0123456789abcdef"
)

// Session returns a browser session's caller on the /api upgrade.
func Session() lodgeapp.Caller {
	return lodgeapp.Caller{Kind: lodgeapp.Session, Login: Login, DisplayName: DisplayName,
		NodeID: NodeID, NodeName: NodeName, Session: SessionID}
}

// X returns a session's caller for /x/ reads, which carry no Lodge-Session.
func X() lodgeapp.Caller {
	c := Session()
	c.Session = ""
	return c
}

// Content returns a content caller.
func Content() lodgeapp.Caller {
	c := X()
	c.Kind = lodgeapp.Content
	return c
}

// Agent returns an MCP caller with token name token and scope.
func Agent(token, scope string) lodgeapp.Caller {
	return lodgeapp.Caller{Kind: lodgeapp.Agent, Token: token, Scope: scope}
}

// Device returns a device caller from an untagged node WhoIs named.
func Device() lodgeapp.Caller {
	return lodgeapp.Caller{Kind: lodgeapp.Device, Login: Login, NodeID: NodeID, NodeName: NodeName}
}

// Lodge is lodge's side of one app socket.
type Lodge struct {
	// Env is the temp root's, with the test's own uid as lodge's.
	Env lodgeapp.Env
	// ID is the app's id, and Socket its socket's path.
	ID     string
	Socket string

	srv    *lodgeapp.Server // set by Start
	client *http.Client
}

// Start serves srv as app id on a socket in a new temp root, as lodge
// would reach it, until the test ends.
func Start(t testing.TB, id string, srv *lodgeapp.Server) *Lodge {
	t.Helper()
	dir := root(t)
	serve(t, lodgeapp.Env{Root: dir, LodgeUID: os.Getuid()}, id, srv)
	// Attach's cleanup, which closes idle connections, runs before
	// serve's, so the server's shutdown waits for none.
	l := Attach(t, id, dir)
	l.srv = srv
	return l
}

// Attach returns lodge's side of app id's socket under root, for an app
// daemon the test started itself with LODGE_ROOT=root.
func Attach(t testing.TB, id string, root string) *Lodge {
	t.Helper()
	env := lodgeapp.Env{Root: root, LodgeUID: os.Getuid()}
	sock := env.SocketPath(id)
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		DisableCompression: true,
		Protocols:          new(http.Protocols),
	}
	tr.Protocols.SetHTTP1(true)
	t.Cleanup(tr.CloseIdleConnections)
	return &Lodge{Env: env, ID: id, Socket: sock, client: &http.Client{Transport: tr}}
}

// root makes a short temp root: a socket's path is at most 108 bytes.
func root(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lodge")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// serve runs srv on app id's socket under env until the test ends.
func serve(t testing.TB, env lodgeapp.Env, id string, srv *lodgeapp.Server) {
	t.Helper()
	ln, err := env.Listen(id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
}

// Do sends req to the app as lodge would: it deletes every Lodge-* header
// req carries and sets c's. The zero Caller sends none, as lodge's /health
// polls do. A relative req.URL is resolved on the socket.
func (l *Lodge) Do(req *http.Request, c lodgeapp.Caller) (*http.Response, error) {
	req.URL.Scheme, req.URL.Host, req.Host = "http", "lodge", "lodge"
	c.SetHeaders(req.Header)
	return l.client.Do(req)
}

// Get sends GET path as c, and returns the status and body. It fails the
// test when the socket does not answer.
func (l *Lodge) Get(t testing.TB, path string, c lodgeapp.Caller) (int, string) {
	t.Helper()
	return l.Request(t, http.MethodGet, path, c)
}

// Request sends method path as c with no body, and returns the status and
// body. It fails the test when the socket does not answer.
func (l *Lodge) Request(t testing.TB, method, path string, c lodgeapp.Caller) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := l.Do(req, c)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp.StatusCode, string(body)
}

// Health polls GET /health as lodge does, with no caller.
func (l *Lodge) Health(ctx context.Context) (lodgeapp.Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/health", nil)
	if err != nil {
		return lodgeapp.Health{}, err
	}
	resp, err := l.Do(req, lodgeapp.Caller{})
	if err != nil {
		return lodgeapp.Health{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return lodgeapp.Health{}, fmt.Errorf("GET /health: %s", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return lodgeapp.Health{}, fmt.Errorf("GET /health: Content-Type %q", ct)
	}
	var h lodgeapp.Health
	if err := json.UnmarshalRead(resp.Body, &h); err != nil {
		return lodgeapp.Health{}, fmt.Errorf("GET /health: %w", err)
	}
	return h, nil
}

// DialAPI opens GET /api as c, the browser's side of a session. The
// connection carries one JSON-RPC message per line: each line written is
// one text frame, and each frame read ends with a newline. It closes when
// ctx ends.
func (l *Lodge) DialAPI(ctx context.Context, c lodgeapp.Caller) (net.Conn, error) {
	h := http.Header{}
	c.SetHeaders(h)
	ws, resp, err := websocket.Dial(ctx, "ws://lodge/api", &websocket.DialOptions{
		HTTPClient:      l.client,
		HTTPHeader:      h,
		Host:            "lodge",
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if resp != nil {
			return nil, &StatusError{Code: resp.StatusCode, Err: err}
		}
		return nil, err
	}
	return wsline.New(ctx, ws), nil
}

// StatusError is DialAPI's error when the app answered the upgrade with a
// status other than 101.
type StatusError struct {
	Code int
	Err  error
}

func (e *StatusError) Error() string { return fmt.Sprintf("status %d: %v", e.Code, e.Err) }
func (e *StatusError) Unwrap() error { return e.Err }

// statusOf returns a DialAPI error's status, or 0.
func statusOf(err error) int {
	if se, ok := errors.AsType[*StatusError](err); ok {
		return se.Code
	}
	return 0
}
