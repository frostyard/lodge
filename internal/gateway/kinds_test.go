package gateway

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/frostyard/lodge/internal/peer"
)

var forgedHeaders = map[string]string{
	"Lodge-Caller": "agent", "Lodge-Login": "mallory@example.com", "Lodge-Token": "root", "Lodge-Scope": "*",
	"Lodge-Session": "0000000000000000", "Lodge-Node-ID": "nX", "Lodge-Whatever": "x", "Lodge_Caller": "agent",
	"Tailscale-User-Login": "x", "X-Forwarded-For": "192.0.2.9", "X-Forwarded-Host": "evil.example",
	"Forwarded": "for=192.0.2.9", "X-Real-IP": "192.0.2.9", "Cookie": "a=b",
}

func checkNoForged(t *testing.T, where string, h http.Header, keep ...string) {
	t.Helper()
	for k := range forgedHeaders {
		ck := http.CanonicalHeaderKey(k)
		for _, want := range keep {
			if ck == http.CanonicalHeaderKey(want) {
				goto next
			}
		}
		if v := h.Values(ck); len(v) > 0 && v[0] == forgedHeaders[k] {
			t.Errorf("%s: the app received the client's %s: %q", where, k, v)
		}
	next:
	}
}

func TestXReads(t *testing.T) {
	h := newHarness(t)
	a := h.get("/app/notes/x/blobs/a%20b?size=2&x=%2F", forgedHeaders)
	if a.status != http.StatusOK || a.body != "app /x/blobs/a b" {
		t.Fatalf("x/: %d %q", a.status, a.body)
	}
	s := h.notes.last(t)
	if s.Method != http.MethodGet || s.Query != "size=2&x=%2F" || s.Host != "notes" {
		t.Errorf("the app saw %+v", s)
	}
	want := map[string]string{"Lodge-Caller": "session", "Lodge-Login": "alice@example.com", "Lodge-Node-Id": "nPhone1CNTRL", "Lodge-Node-Name": "phone"}
	for k, v := range want {
		if got := s.Header.Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if s.Header.Get("Lodge-Session") != "" {
		t.Error("an x/ read carries Lodge-Session")
	}
	checkNoForged(t, "x/", s.Header)
	if a := h.do(h.req(http.MethodHead, "/app/notes/x/a", nil)); a.status != http.StatusOK || h.notes.last(t).Method != http.MethodHead {
		t.Errorf("HEAD x/: %d", a.status)
	}
	expect(t, h.get("/app/plain/x/a", nil), http.StatusBadGateway, "app plain cannot be reached")
}

func TestProxyEncodesIdentity(t *testing.T) {
	h := newHarness(t)
	h.who.set(func(p *peer.Peer) { p.Login, p.DisplayName = "zoë+dev@example.com", "" })
	h.allow.AllowLogin(t.Context(), "zoë+dev@example.com")
	h.get("/app/notes/x/a", nil)
	s := h.notes.last(t)
	if got := s.Header.Get("Lodge-Login"); got != "zo%C3%AB+dev@example.com" {
		t.Errorf("Lodge-Login = %q", got)
	}
	if _, ok := s.Header["Lodge-Display-Name"]; ok {
		t.Error("an empty display name was sent")
	}
}

func (h *harness) contentGet(path string, hdr map[string]string) answer {
	return h.do(h.reqTo(h.content, contentHost, http.MethodGet, path, hdr))
}

const notesContentPolicy = "default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; sandbox; frame-ancestors https://lodge.example"

func TestContentChecks(t *testing.T) {
	h := newHarness(t)
	r := h.reqTo(h.content, "evil.example", http.MethodGet, "/notes/b", nil)
	expect(t, h.do(r), http.StatusForbidden, "content address")
	r = h.reqTo(h.content, lodgeHost, http.MethodGet, "/notes/b", nil)
	expect(t, h.do(r), http.StatusForbidden, "content address")
	r = h.reqTo(h.content, contentHost+":443", http.MethodGet, "/notes/b", nil)
	if a := h.do(r); a.status != http.StatusOK {
		t.Errorf("CONTENT:443: %d", a.status)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		a := h.do(h.reqTo(h.content, contentHost, m, "/notes/b", nil))
		expect(t, a, http.StatusMethodNotAllowed, "")
		if a.header.Get("Allow") != "GET, HEAD" {
			t.Errorf("Allow = %q", a.header.Get("Allow"))
		}
	}
	expect(t, h.contentGet("/notes/b", map[string]string{"Sec-Fetch-Site": "cross-site"}), http.StatusForbidden, "another site")
	for _, site := range []string{"same-site", "same-origin", "none"} {
		if a := h.contentGet("/notes/b", map[string]string{"Sec-Fetch-Site": site}); a.status != http.StatusOK {
			t.Errorf("Sec-Fetch-Site %s: %d", site, a.status)
		}
	}
	// No Origin rule on content: a frame from lodge's page may send one.
	if a := h.contentGet("/notes/b", map[string]string{"Origin": lodgeOrigin}); a.status != http.StatusOK {
		t.Errorf("with lodge's Origin: %d", a.status)
	}
	expect(t, h.contentGet("/plain/b", nil), http.StatusNotFound, "not found")
	expect(t, h.contentGet("/nosuch/b", nil), http.StatusNotFound, "not found")
	expect(t, h.contentGet("/notes", nil), http.StatusNotFound, "not found")
	expect(t, h.contentGet("/", nil), http.StatusNotFound, "not found")
	expect(t, h.contentGet("/off/b", nil), http.StatusServiceUnavailable, "disabled")
	// The checks run before the app is looked up.
	h.who.fail(errors.New("no peer"))
	expect(t, h.contentGet("/nosuch/b", nil), http.StatusForbidden, "cannot name")
}

func TestContentProxy(t *testing.T) {
	h := newHarness(t)
	h.notes.handle = func(w http.ResponseWriter, r *http.Request) bool {
		hd := w.Header()
		hd.Set("Content-Type", "image/png")
		hd.Set("Content-Disposition", "attachment")
		hd.Set("Content-Range", "bytes 0-1/4")
		hd.Set("Accept-Ranges", "bytes")
		hd.Set("ETag", `"abc"`)
		hd.Set("Last-Modified", "Sun, 18 Oct 2026 12:00:00 GMT")
		hd.Set("Content-Security-Policy", "img-src 'none'")
		hd.Set("Set-Cookie", "a=b")
		hd.Set("Access-Control-Allow-Origin", "*")
		hd.Set("X-Frame-Options", "ALLOWALL")
		hd.Set("X-Other", "dropped")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("PN"))
		return true
	}
	hdr := map[string]string{"Range": "bytes=0-1"}
	for k, v := range forgedHeaders {
		hdr[k] = v
	}
	a := h.contentGet("/notes/blobs/abc.png?download=1", hdr)
	if a.status != http.StatusPartialContent || a.body != "PN" {
		t.Fatalf("content: %d %q", a.status, a.body)
	}
	s := h.notes.last(t)
	if s.Path != "/content/blobs/abc.png" || s.Query != "download=1" || s.Header.Get("Range") != "bytes=0-1" {
		t.Errorf("the app saw %+v", s)
	}
	for k, v := range map[string]string{"Lodge-Caller": "content", "Lodge-Login": "alice@example.com", "Lodge-Node-Id": "nPhone1CNTRL",
		"Lodge-Node-Name": "phone", "Lodge-Display-Name": "Alice%20Zo%C3%AB"} {
		if got := s.Header.Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	checkNoForged(t, "content", s.Header)
	want := map[string]string{
		"Content-Type": "image/png", "Content-Disposition": "attachment", "Content-Range": "bytes 0-1/4", "Accept-Ranges": "bytes",
		"Etag": `"abc"`, "Last-Modified": "Sun, 18 Oct 2026 12:00:00 GMT", "Cache-Control": "private, no-cache",
		"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Cross-Origin-Resource-Policy": "same-site",
		"Cross-Origin-Opener-Policy": "same-origin",
	}
	for k, v := range want {
		if got := a.header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if got := a.header.Values("Content-Security-Policy"); len(got) != 2 || got[0] != "img-src 'none'" || got[1] != notesContentPolicy {
		t.Errorf("CSP = %q", got)
	}
	for _, k := range []string{"Set-Cookie", "Access-Control-Allow-Origin", "X-Frame-Options", "X-Other"} {
		if v := a.header.Get(k); v != "" {
			t.Errorf("content passes %s: %q", k, v)
		}
	}
	// The app's own Cache-Control passes.
	h.notes.handle = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Cache-Control", "private, max-age=60")
		w.Write([]byte("x"))
		return true
	}
	if got := h.contentGet("/notes/b", nil).header.Get("Cache-Control"); got != "private, max-age=60" {
		t.Errorf("the app's Cache-Control = %q", got)
	}
}

// D6: contentLinks lets a user's click open a new window, and nothing
// else; without it the policy is plain sandbox.
func TestContentLinksPolicy(t *testing.T) {
	h := newHarness(t)
	plain := h.contentGet("/notes/b", nil).header.Values("Content-Security-Policy")
	if len(plain) != 1 || plain[0] != notesContentPolicy {
		t.Errorf("notes' policy = %q", plain)
	}
	links := h.contentGet("/mail/m/1", nil)
	want := "default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; sandbox allow-popups allow-popups-to-escape-sandbox; frame-ancestors https://lodge.example"
	if got := links.header.Values("Content-Security-Policy"); len(got) != 1 || got[0] != want {
		t.Errorf("mail's policy = %q", got)
	}
	for _, csp := range append(plain, links.header.Values("Content-Security-Policy")...) {
		for _, never := range []string{"allow-scripts", "allow-same-origin", "allow-forms", "allow-top-navigation"} {
			if strings.Contains(csp, never) {
				t.Errorf("a content policy has %s", never)
			}
		}
	}
	// Errors carry the app's policy too.
	r := h.reqTo(h.content, contentHost, http.MethodPost, "/mail/m/1", nil)
	if got := h.do(r).header.Get("Content-Security-Policy"); got != want {
		t.Errorf("an error's policy = %q", got)
	}
}

func (h *harness) issue(name, app, scope string) string {
	h.t.Helper()
	secret, err := h.tokens.Issue(h.t.Context(), name, app, scope)
	if err != nil {
		h.t.Fatal(err)
	}
	return secret
}

func (h *harness) mcpReq(method, app, secret string, hdr map[string]string) *http.Request {
	r := h.req(method, "/mcp/"+app, hdr)
	if secret != "" {
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	return r
}

func TestMCPChecks(t *testing.T) {
	h := newHarness(t)
	secret := h.issue("agent", "notes", "vault=1")
	r := h.mcpReq(http.MethodPost, "notes", secret, nil)
	r.Host = "evil.example"
	expect(t, h.do(r), http.StatusForbidden, "address")
	expect(t, h.do(h.mcpReq(http.MethodPost, "notes", secret, map[string]string{"Origin": lodgeOrigin})), http.StatusForbidden, "browsers")
	for _, m := range []string{http.MethodPut, http.MethodPatch, http.MethodHead} {
		a := h.do(h.mcpReq(m, "notes", secret, nil))
		if a.status != http.StatusMethodNotAllowed || a.header.Get("Allow") != "POST, GET, DELETE" {
			t.Errorf("%s: %d Allow %q", m, a.status, a.header.Get("Allow"))
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		if a := h.do(h.mcpReq(m, "notes", secret, nil)); a.status != http.StatusOK {
			t.Errorf("%s: %d %q", m, a.status, a.body)
		}
	}
	// Tags and the allow-lists are not consulted.
	h.who.set(func(p *peer.Peer) { p.Tagged, p.Login, p.NodeID = true, "tagged-devices", "nCI" })
	if a := h.do(h.mcpReq(http.MethodPost, "notes", secret, nil)); a.status != http.StatusOK {
		t.Errorf("a tagged, unlisted node: %d %q", a.status, a.body)
	}
	// Checks a and b are.
	h.who.set(func(p *peer.Peer) { p.Addrs = []netip.Addr{netip.MustParseAddr("192.0.2.7")} })
	expect(t, h.do(h.mcpReq(http.MethodPost, "notes", secret, nil)), http.StatusForbidden, "forwarded")
	h.who.fail(peer.ErrUnknown)
	expect(t, h.do(h.mcpReq(http.MethodPost, "notes", secret, nil)), http.StatusForbidden, "cannot name")
}

func TestMCPTokens(t *testing.T) {
	h := newHarness(t)
	secret := h.issue("agent", "notes", "vault=1")
	mailSecret := h.issue("mailer", "mail", "x")
	revoked := h.issue("old", "notes", "x")
	h.tokens.Revoke(t.Context(), "old")
	for name, auth := range map[string]string{
		"missing": "", "malformed": "Bearer lgm_nope", "basic": "Basic " + secret, "unknown": "Bearer lgm_" + strings.Repeat("A", 43),
		"revoked": "Bearer " + revoked, "another app": "Bearer " + mailSecret, "the hash": "Bearer " + strings.Repeat("a", 64),
	} {
		r := h.req(http.MethodPost, "/mcp/notes", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		a := h.do(r)
		if a.status != http.StatusUnauthorized || a.header.Get("WWW-Authenticate") != "Bearer" || strings.TrimSpace(a.body) != "unauthorized" {
			t.Errorf("%s token: %d %q %q", name, a.status, a.header.Get("WWW-Authenticate"), a.body)
		}
	}
	if h.notes.count() != 0 {
		t.Error("a refused token reached the app")
	}
	// The app: no mcp is 404, disabled is 503.
	plain := h.issue("p", "plain", "x")
	expect(t, h.do(h.mcpReq(http.MethodPost, "plain", plain, nil)), http.StatusNotFound, "")
	off := h.issue("o", "off", "x")
	expect(t, h.do(h.mcpReq(http.MethodPost, "off", off, nil)), http.StatusServiceUnavailable, "disabled")
	_ = secret
}

func TestMCPProxy(t *testing.T) {
	h := newHarness(t)
	secret := h.issue("claude-code@laptop", "notes", "vault=1;write")
	h.notes.handle = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Mcp-Session-Id", "s-123")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		return true
	}
	hdr := map[string]string{"Mcp-Session-Id": "s-123", "Mcp-Protocol-Version": "2025-06-18"}
	for k, v := range forgedHeaders {
		hdr[k] = v
	}
	a := h.do(h.mcpReq(http.MethodPost, "notes", secret, hdr))
	if a.status != http.StatusOK || a.header.Get("Mcp-Session-Id") != "s-123" {
		t.Fatalf("mcp: %d %v", a.status, a.header)
	}
	s := h.notes.last(t)
	if s.Path != "/mcp" || s.Method != http.MethodPost {
		t.Errorf("the app saw %s %s", s.Method, s.Path)
	}
	for k, v := range map[string]string{"Lodge-Caller": "agent", "Lodge-Token": "claude-code@laptop", "Lodge-Scope": "vault=1;write",
		"Mcp-Session-Id": "s-123", "Mcp-Protocol-Version": "2025-06-18"} {
		if got := s.Header.Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if s.Header.Get("Authorization") != "" {
		t.Error("the app received the token")
	}
	for _, k := range []string{"Lodge-Login", "Lodge-Node-Id", "Lodge-Session"} {
		if s.Header.Get(k) != "" {
			t.Errorf("an agent request carries %s", k)
		}
	}
	checkNoForged(t, "mcp", s.Header, "Lodge-Caller", "Lodge-Token", "Lodge-Scope")
	list, _ := h.tokens.List(t.Context())
	if list[0].LastUsed.IsZero() {
		t.Error("the token's use was not recorded")
	}
}

// Streams are relayed without buffering, and revoking the token closes
// them at once.
func TestMCPStreamAndRevocation(t *testing.T) {
	h := newHarness(t)
	secret := h.issue("agent", "notes", "vault=1")
	appDone := make(chan struct{})
	h.notes.handle = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(appDone)
		return true
	}
	resp, err := http.DefaultTransport.RoundTrip(h.mcpReq(http.MethodGet, "notes", secret, map[string]string{"Accept": "text/event-stream"}))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("the first event arrived as %q, %v", line, err)
	}
	if n := h.g.streams.count("agent"); n != 1 {
		t.Fatalf("%d streams for the token, want 1", n)
	}
	if err := h.tokens.Revoke(t.Context(), "agent"); err != nil {
		t.Fatal(err)
	}
	if n := h.g.EndToken("agent"); n != 1 {
		t.Errorf("EndToken closed %d", n)
	}
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream stayed open after revocation")
	}
	select {
	case <-appDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the app's request stayed open")
	}
	expect(t, h.do(h.mcpReq(http.MethodPost, "notes", secret, nil)), http.StatusUnauthorized, "")
}

func TestDevice(t *testing.T) {
	h := newHarness(t)
	h.notes.handle = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Upgrade") != "" {
			return false
		}
		w.Header().Set("Set-Cookie", "a=b")
		w.Header().Set("X-App", "kept")
		w.WriteHeader(http.StatusCreated)
		io.Copy(w, r.Body)
		return true
	}
	r := h.req(http.MethodPost, "/device/notes/sync/pair?x=1", forgedHeaders)
	r.Header.Set("Authorization", "Bearer device-token")
	r.Header.Set("Origin", "notesd://device")
	r.Body = io.NopCloser(strings.NewReader("hello"))
	a := h.do(r)
	if a.status != http.StatusCreated || a.body != "hello" {
		t.Fatalf("device: %d %q", a.status, a.body)
	}
	s := h.notes.last(t)
	if s.Path != "/device/sync/pair" || s.Query != "x=1" || s.Method != http.MethodPost {
		t.Errorf("the app saw %+v", s)
	}
	for k, v := range map[string]string{"Lodge-Caller": "device", "Authorization": "Bearer device-token", "Origin": "notesd://device",
		"Lodge-Node-Id": "nPhone1CNTRL", "Lodge-Node-Name": "phone", "Lodge-Login": "alice@example.com"} {
		if got := s.Header.Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if s.Header.Get("Lodge-Display-Name") != "" {
		t.Error("a device request carries the display name")
	}
	checkNoForged(t, "device", s.Header, "Lodge-Caller", "Lodge-Node-ID", "Lodge-Login")
	if a.header.Get("Set-Cookie") != "" || a.header.Get("X-Content-Type-Options") != "nosniff" ||
		a.header.Get("Cross-Origin-Resource-Policy") != "same-origin" || a.header.Get("X-App") != "kept" {
		t.Errorf("device response headers: %v", a.header)
	}
	// Any method; no identity checks: a tagged, unlisted, forwarding node
	// reaches the app, with the peer for display only.
	h.who.set(func(p *peer.Peer) {
		p.Tagged, p.Login, p.NodeID, p.Addrs = true, "tagged-devices", "nCI", []netip.Addr{netip.MustParseAddr("192.0.2.7")}
	})
	if a := h.do(h.req(http.MethodPut, "/device/notes/x", nil)); a.status != http.StatusCreated {
		t.Errorf("a tagged device: %d", a.status)
	}
	s = h.notes.last(t)
	if s.Header.Get("Lodge-Node-Id") != "nCI" || s.Header.Get("Lodge-Login") != "" {
		t.Errorf("a tagged device: node %q login %q", s.Header.Get("Lodge-Node-Id"), s.Header.Get("Lodge-Login"))
	}
	h.who.fail(peer.ErrUnknown)
	if a := h.do(h.req(http.MethodDelete, "/device/notes", nil)); a.status != http.StatusCreated {
		t.Errorf("an unnamed peer: %d", a.status)
	}
	s = h.notes.last(t)
	if s.Path != "/device" || s.Header.Get("Lodge-Node-Id") != "" || s.Header.Get("Lodge-Caller") != "device" {
		t.Errorf("an unnamed peer: %s %v", s.Path, s.Header)
	}
	// Host, and the app.
	r = h.req(http.MethodGet, "/device/notes/x", nil)
	r.Host = "evil.example"
	expect(t, h.do(r), http.StatusForbidden, "address")
	expect(t, h.get("/device/mail/x", nil), http.StatusNotFound, "")
	expect(t, h.get("/device/nosuch/x", nil), http.StatusNotFound, "")
	expect(t, h.get("/device/off/x", nil), http.StatusServiceUnavailable, "disabled")
}

func TestDeviceWebSocket(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("Origin", "notesd://device")
	hdr.Set("Authorization", "Bearer device-token")
	hdr.Set("Lodge-Login", "mallory@example.com")
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.lodge.URL, "http")+"/device/notes/sync?v=1", &websocket.DialOptions{
		HTTPHeader: hdr, Host: lodgeHost, Subprotocols: []string{"notes-sync"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if c.Subprotocol() != "notes-sync" {
		t.Errorf("subprotocol = %q", c.Subprotocol())
	}
	if err := c.Write(ctx, websocket.MessageText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if _, got, err := c.Read(ctx); err != nil || string(got) != "hi" {
		t.Fatalf("echo: %q %v", got, err)
	}
	s := h.notes.last(t)
	if s.Path != "/device/sync" || s.Query != "v=1" || s.Header.Get("Authorization") != "Bearer device-token" ||
		s.Header.Get("Origin") != "notesd://device" || s.Header.Get("Lodge-Caller") != "device" || s.Header.Get("Lodge-Login") != "alice@example.com" {
		t.Errorf("the app saw %s?%s %v", s.Path, s.Query, s.Header)
	}
	if err := c.Write(ctx, websocket.MessageBinary, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if code := closeCode(t, c); code != websocket.StatusUnsupportedData {
		t.Errorf("a binary frame: close %d, want 1003", code)
	}
}
