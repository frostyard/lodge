package gateway

import (
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/lodge/internal/peer"
)

// Every browser route runs every browser check. Each case fails one.
var browserRoutes = []string{"/", "/notes", "/notes/1/Projects/Plan.md", "/lodge/apps", "/lodge/shell.js", "/app/notes/", "/app/notes/style.css", "/app/notes/x/read"}

func TestBrowserHost(t *testing.T) {
	h := newHarness(t)
	for _, p := range browserRoutes {
		for _, host := range []string{"evil.example", "lodge.example.evil", "127.0.0.1", lodgeHost + ":8443", "LODGE.EXAMPLE"} {
			r := h.req(http.MethodGet, p, nil)
			r.Host = host
			expect(t, h.do(r), http.StatusForbidden, "this is not lodge's address")
		}
		r := h.req(http.MethodGet, p, nil)
		r.Host = lodgeHost + ":443"
		if a := h.do(r); a.status != http.StatusOK {
			t.Errorf("%s with Host LODGE:443: %d %s", p, a.status, a.body)
		}
		if a := h.get(p, nil); a.status != http.StatusOK {
			t.Errorf("%s with Host LODGE: %d %s", p, a.status, a.body)
		}
	}
}

func TestBrowserOrigin(t *testing.T) {
	h := newHarness(t)
	for _, p := range browserRoutes {
		for _, o := range []string{"https://evil.example", "null", "http://" + lodgeHost, lodgeOrigin + ":443", lodgeOrigin + "/", "https://" + contentHost} {
			expect(t, h.get(p, map[string]string{"Origin": o}), http.StatusForbidden, "another origin")
		}
		if a := h.get(p, map[string]string{"Origin": lodgeOrigin}); a.status != http.StatusOK {
			t.Errorf("%s with lodge's Origin: %d %s", p, a.status, a.body)
		}
		// Absent on a plain GET is fine (M4 review M2).
		if a := h.get(p, nil); a.status != http.StatusOK {
			t.Errorf("%s without Origin: %d", p, a.status)
		}
	}
	// Two Origin headers, one of them lodge's.
	r := h.req(http.MethodGet, "/app/notes/", nil)
	r.Header.Add("Origin", lodgeOrigin)
	r.Header.Add("Origin", "https://evil.example")
	expect(t, h.do(r), http.StatusForbidden, "another origin")
}

func TestBrowserFetchMetadata(t *testing.T) {
	h := newHarness(t)
	for _, p := range browserRoutes {
		for _, site := range []string{"cross-site", "same-site", "bogus"} {
			expect(t, h.get(p, map[string]string{"Sec-Fetch-Site": site}), http.StatusForbidden, "another site")
		}
		for _, site := range []string{"same-origin", "none"} {
			if a := h.get(p, map[string]string{"Sec-Fetch-Site": site}); a.status != http.StatusOK {
				t.Errorf("%s with Sec-Fetch-Site %s: %d", p, site, a.status)
			}
		}
	}
	nav := map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}
	// The exception: a top-level navigation to a shell page, from any site.
	for _, p := range []string{"/", "/notes", "/notes/1/Projects/Plan.md"} {
		if a := h.get(p, nav); a.status != http.StatusOK {
			t.Errorf("a cross-site navigation to %s: %d %s", p, a.status, a.body)
		}
		// HEAD, a frame or a fetch is no navigation.
		expect(t, h.do(h.req(http.MethodHead, p, nav)), http.StatusForbidden, "")
		for k, v := range map[string]string{"Sec-Fetch-Dest": "iframe", "Sec-Fetch-Mode": "cors"} {
			hdr := map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}
			hdr[k] = v
			expect(t, h.get(p, hdr), http.StatusForbidden, "another site")
		}
	}
	// Not for the shell's files or an app's.
	for _, p := range []string{"/lodge/apps", "/lodge/shell.js", "/app/notes/", "/app/notes/x/read"} {
		expect(t, h.get(p, nav), http.StatusForbidden, "another site")
	}
}

func TestBrowserMethod(t *testing.T) {
	h := newHarness(t)
	for _, p := range browserRoutes {
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
			a := h.do(h.req(m, p, nil))
			expect(t, a, http.StatusMethodNotAllowed, "not allowed")
			if a.header.Get("Allow") != "GET, HEAD" {
				t.Errorf("%s %s: Allow %q", m, p, a.header.Get("Allow"))
			}
		}
		if a := h.do(h.req(http.MethodHead, p, nil)); a.status != http.StatusOK || a.body != "" {
			t.Errorf("HEAD %s: %d %q", p, a.status, a.body)
		}
	}
	a := h.do(h.req(http.MethodHead, "/app/notes/api", nil))
	if a.status != http.StatusMethodNotAllowed || a.header.Get("Allow") != "GET" {
		t.Errorf("HEAD /app/notes/api: %d, Allow %q", a.status, a.header.Get("Allow"))
	}
}

func TestBrowserChecksRunInOrder(t *testing.T) {
	h := newHarness(t)
	h.who.fail(errors.New("no peer"))
	// A wrong Host fails before the Origin, the Origin before the method,
	// and the method before identity.
	r := h.req(http.MethodPost, "/app/notes/", map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"})
	r.Host = "evil.example"
	expect(t, h.do(r), http.StatusForbidden, "address")
	expect(t, h.do(h.req(http.MethodPost, "/app/notes/", map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"})), http.StatusForbidden, "origin")
	expect(t, h.do(h.req(http.MethodPost, "/app/notes/", map[string]string{"Sec-Fetch-Site": "cross-site"})), http.StatusForbidden, "site")
	expect(t, h.do(h.req(http.MethodPost, "/app/notes/", nil)), http.StatusMethodNotAllowed, "")
	expect(t, h.get("/app/notes/", nil), http.StatusForbidden, "cannot name")
}

func TestIdentity(t *testing.T) {
	cases := map[string]struct {
		set  func(h *harness)
		line string
	}{
		"a: WhoIs names no peer": {func(h *harness) { h.who.fail(peer.ErrUnknown) }, "lodge cannot name this peer"},
		"b: forwarded traffic": {func(h *harness) {
			h.who.set(func(p *peer.Peer) { p.Addrs = []netip.Addr{netip.MustParseAddr("192.0.2.7")} })
		}, "forwarded traffic is refused"},
		"c: tagged node":       {func(h *harness) { h.who.set(func(p *peer.Peer) { p.Tagged = true }) }, "tagged nodes cannot sign in"},
		"d: login not allowed": {func(h *harness) { h.who.set(func(p *peer.Peer) { p.Login = "bob@example.com" }) }, "login bob@example.com is not allowed: lodgectl allow login bob@example.com"},
		"e: node not allowed": {func(h *harness) {
			h.who.set(func(p *peer.Peer) { p.NodeID, p.NodeName = "nLaptop", "laptop" })
		}, "node laptop (nLaptop) is not allowed: lodgectl allow node nLaptop"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			c.set(h)
			for _, p := range browserRoutes {
				a := h.get(p, nil)
				if a.status != http.StatusForbidden || strings.TrimSpace(a.body) != c.line {
					t.Errorf("%s: %d %q, want 403 %q", p, a.status, a.body, c.line)
				}
				if a.header.Get("Content-Type") != "text/plain; charset=utf-8" {
					t.Errorf("%s: error Content-Type %q", p, a.header.Get("Content-Type"))
				}
			}
			a := h.do(h.reqTo(h.content, contentHost, http.MethodGet, "/notes/blob", nil))
			if a.status != http.StatusForbidden || strings.TrimSpace(a.body) != c.line {
				t.Errorf("content: %d %q, want 403 %q", a.status, a.body, c.line)
			}
		})
	}
}

// The allow-lists are read on every request, even on a connection whose
// WhoIs answer is cached.
func TestAllowListsReadEveryRequest(t *testing.T) {
	h := newHarness(t)
	c := &http.Client{Transport: &http.Transport{}}
	get := func() int {
		r := h.req(http.MethodGet, "/app/notes/", nil)
		r.Close = false
		resp, err := c.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body) // a drained body keeps the connection
		resp.Body.Close()
		return resp.StatusCode
	}
	if s := get(); s != http.StatusOK {
		t.Fatalf("first request: %d", s)
	}
	if err := h.allow.DisallowLogin(t.Context(), "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if s := get(); s != http.StatusForbidden {
		t.Errorf("after disallow: %d, want 403", s)
	}
	if err := h.allow.AllowLogin(t.Context(), "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := h.allow.DisallowNode(t.Context(), "nPhone1CNTRL"); err != nil {
		t.Fatal(err)
	}
	if s := get(); s != http.StatusForbidden {
		t.Errorf("after disallowing the node: %d, want 403", s)
	}
	if n := h.who.calls.Load(); n != 1 {
		t.Errorf("WhoIs was asked %d times on one connection, want 1", n)
	}
}

func TestWhoIsCachedPerConnectionFor60s(t *testing.T) {
	h := newHarness(t)
	c := &http.Client{Transport: &http.Transport{}}
	get := func(c *http.Client) {
		r := h.req(http.MethodGet, "/lodge/apps", nil)
		r.Close = false
		resp, err := c.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body) // a drained body keeps the connection
		resp.Body.Close()
	}
	get(c)
	h.clock.advance(WhoIsCacheFor - time.Second)
	get(c)
	if n := h.who.calls.Load(); n != 1 {
		t.Fatalf("within 60 s: %d WhoIs calls, want 1", n)
	}
	h.clock.advance(time.Second)
	get(c)
	if n := h.who.calls.Load(); n != 2 {
		t.Fatalf("at 60 s: %d WhoIs calls, want 2", n)
	}
	// Another connection asks again.
	get(&http.Client{Transport: &http.Transport{}})
	if n := h.who.calls.Load(); n != 3 {
		t.Errorf("a new connection: %d WhoIs calls, want 3", n)
	}
}

// Identity comes from WhoIs alone: forged headers, cookies and queries
// change nothing.
func TestForgedIdentityChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.who.set(func(p *peer.Peer) { p.Login = "mallory@example.com" })
	forged := map[string]string{
		"Tailscale-User-Login": "alice@example.com", "Tailscale-User-Name": "Alice", "Tailscale-App-Capabilities": "{}",
		"X-Forwarded-For": "127.0.0.1", "X-Forwarded-User": "alice@example.com", "Forwarded": "for=127.0.0.1",
		"X-Real-IP": "127.0.0.1", "Cookie": "login=alice@example.com", "Lodge-Login": "alice@example.com",
		"Authorization": "Bearer x",
	}
	expect(t, h.get("/app/notes/?login=alice@example.com", forged), http.StatusForbidden, "mallory@example.com is not allowed")
}

func TestUnknownPaths(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/nosuchapp", "/nosuchapp/x", "/app/nosuchapp/", "/app/nosuchapp/api"} {
		a := h.get(p, nil)
		expect(t, a, http.StatusNotFound, "no app is installed")
	}
	for _, p := range []string{"/Bad", "/app/", "/app/Bad/x", "/mcp/", "/mcp/notes/x", "/device/", "/x/../app", "/app/notes/%2e%2e/secret", "/averyveryverylongappid"} {
		a := h.get(p, nil)
		if a.status != http.StatusNotFound {
			t.Errorf("%s: %d %q, want 404", p, a.status, a.body)
		}
		if a.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no nosniff on a 404", p)
		}
	}
	// A disabled app's shell page loads; its frame and files are 503.
	if a := h.get("/off", nil); a.status != http.StatusOK {
		t.Errorf("/off: %d", a.status)
	}
	for _, p := range []string{"/app/off/", "/app/off/icon.svg", "/app/off/x/a", "/app/off/api"} {
		expect(t, h.get(p, nil), http.StatusServiceUnavailable, "app off is disabled: contract 9")
	}
}
