package kittest

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/lodge/kit/lodgeapp"
	"github.com/frostyard/lodge/kit/manifest"
)

// paths are one path of each route on the socket, with the kind it admits.
var paths = []struct {
	path string
	kind lodgeapp.Kind
}{
	{"/api", lodgeapp.Session},
	{"/x/kittest", lodgeapp.Session},
	{"/content/kittest", lodgeapp.Content},
	{"/mcp", lodgeapp.Agent},
	{"/device/kittest", lodgeapp.Device},
	{"/kittest", ""}, // no route: it admits no kind
}

// callers are one caller of each kind.
func callers() []lodgeapp.Caller {
	return []lodgeapp.Caller{X(), Content(), Agent("kittest", "kittest"), Device()}
}

// CheckRules checks the contract's socket rules against the app l serves,
// whose manifest is m, as subtests (app-contract.md, What an app serves on
// its socket):
//
//   - GET /health answers 200 and a valid Health without Lodge-Caller;
//   - a request without Lodge-Caller, or with a malformed or repeated one,
//     answers 403;
//   - each path admits its one caller kind, and every other kind answers
//     403;
//   - errors are one line of text/plain;
//   - /api upgrades for a session with Lodge-Session, and refuses one
//     without it;
//   - the routes the manifest's flags promise are served, and the rest
//     answer 404;
//   - with Start, a peer whose uid is not lodge's is closed before it is
//     answered.
func CheckRules(t *testing.T, l *Lodge, m manifest.Manifest) {
	t.Helper()
	if err := m.Validate(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.ID != l.ID {
		t.Fatalf("manifest id %q, socket of %q", m.ID, l.ID)
	}

	t.Run("health needs no caller", func(t *testing.T) {
		h, err := l.Health(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case h.OK && (h.Version == "" || h.Protocol < 1):
			t.Errorf("health %+v: an OK app reports its version and protocol", h)
		case !h.OK && h.Reason == "":
			t.Errorf("health %+v: an app that is not OK says why", h)
		}
		for _, c := range callers() {
			if code, _ := l.Get(t, "/health", c); code != http.StatusOK {
				t.Errorf("GET /health as %s = %d, want 200", c.Kind, code)
			}
		}
	})

	t.Run("no caller is refused", func(t *testing.T) {
		for _, p := range paths {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, p.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := l.Do(req, lodgeapp.Caller{})
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("GET %s without Lodge-Caller = %d, want 403", p.path, resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
				t.Errorf("GET %s's error is %q, want text/plain", p.path, ct)
			}
		}
	})

	t.Run("forged callers are refused", func(t *testing.T) {
		for _, h := range []http.Header{
			{lodgeapp.HeaderCaller: {"root"}},
			{lodgeapp.HeaderCaller: {"local"}},
			{lodgeapp.HeaderCaller: {"Session"}},
			{lodgeapp.HeaderCaller: {"device", "device"}},
			{lodgeapp.HeaderCaller: {"agent"}, lodgeapp.HeaderToken: {"a token"}, lodgeapp.HeaderScope: {"s"}},
			{lodgeapp.HeaderCaller: {"session"}, lodgeapp.HeaderNodeName: {NodeName}, lodgeapp.HeaderNodeID: {NodeID}},
		} {
			for _, path := range []string{"/x/kittest", "/device/kittest", "/mcp"} {
				code := l.raw(t, path, h)
				if code != http.StatusForbidden {
					t.Errorf("GET %s with %v = %d, want 403", path, h, code)
				}
			}
		}
	})

	t.Run("each path admits one kind", func(t *testing.T) {
		for _, p := range paths {
			for _, c := range callers() {
				if c.Kind == p.kind {
					continue
				}
				if code, _ := l.Get(t, p.path, c); code != http.StatusForbidden {
					t.Errorf("GET %s as %s = %d, want 403", p.path, c.Kind, code)
				}
			}
		}
	})

	t.Run("api upgrades for a session", func(t *testing.T) {
		nc, err := l.DialAPI(t.Context(), Session())
		if err != nil {
			t.Fatalf("GET /api as a session: %v", err)
		}
		_ = nc.Close()
		if _, err := l.DialAPI(t.Context(), X()); statusOf(err) != http.StatusForbidden {
			t.Errorf("GET /api without Lodge-Session = %v, want 403", err)
		}
		for _, c := range callers()[1:] {
			if _, err := l.DialAPI(t.Context(), c); statusOf(err) != http.StatusForbidden {
				t.Errorf("GET /api as %s = %v, want 403", c.Kind, err)
			}
		}
	})

	t.Run("the manifest's routes are served", func(t *testing.T) {
		for _, r := range []struct {
			flag   bool
			name   string
			path   string
			caller lodgeapp.Caller
		}{
			{m.Content, "content", "/content/kittest", Content()},
			{m.MCP, "mcp", "/mcp", Agent("kittest", "kittest")},
			{m.Device, "device", "/device/kittest", Device()},
		} {
			code, body := l.Get(t, r.path, r.caller)
			served := code != http.StatusNotFound || !strings.Contains(body, "does not serve")
			if served != r.flag {
				t.Errorf("GET %s as %s = %d %q, but the manifest's %s is %v", r.path, r.caller.Kind, code, strings.TrimSpace(body), r.name, r.flag)
			}
		}
	})

	t.Run("a peer that is not lodge is closed", func(t *testing.T) {
		if l.srv == nil {
			t.Skip("needs the Server, which Start has")
		}
		other := lodgeapp.Env{Root: root(t), LodgeUID: os.Getuid() + 1}
		serve(t, other, l.ID, l.srv)
		nc, err := net.Dial("unix", other.SocketPath(l.ID))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = nc.Close() }()
		_ = nc.SetDeadline(time.Now().Add(10 * time.Second))
		_, _ = io.WriteString(nc, "GET /health HTTP/1.1\r\nHost: lodge\r\n\r\n")
		if n, err := nc.Read(make([]byte, 1)); n != 0 || isTimeout(err) {
			t.Errorf("another uid's connection got %d bytes, %v; want it closed", n, err)
		}
	})
}

// raw sends GET path with exactly the headers h, for forgeries Caller
// cannot express, and returns the status.
func (l *Lodge) raw(t *testing.T, path string, h http.Header) int {
	t.Helper()
	nc, err := net.Dial("unix", l.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = nc.Close() }()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://lodge"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = h.Clone()
	req.Close = true
	if err := req.Write(nc); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(nc), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func isTimeout(err error) bool {
	ne, ok := errors.AsType[net.Error](err)
	return ok && ne.Timeout()
}
