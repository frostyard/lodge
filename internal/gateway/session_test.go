package gateway

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/frostyard/lodge/internal/peer"
	"github.com/frostyard/lodge/internal/sessions"
)

func TestSessionRelaysWithCallerHeaders(t *testing.T) {
	h := newHarness(t)
	c, _, err := h.wsDial("/app/notes/api", map[string]string{"Lodge-Login": "mallory@example.com", "Lodge-Session": "0000000000000000",
		"Tailscale-User-Login": "x", "Cookie": "a=b", "Authorization": "Bearer y", "X-Forwarded-For": "192.0.2.9"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, msg := range []string{`{"jsonrpc":"2.0","id":1,"method":"rpc.hello"}`, "second"} {
		if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			t.Fatal(err)
		}
		typ, got, err := c.Read(ctx)
		if err != nil || typ != websocket.MessageText || string(got) != msg {
			t.Fatalf("echo = %v %q %v", typ, got, err)
		}
	}
	s := h.notes.last(t)
	if s.Path != "/api" || s.Host != "notes" {
		t.Errorf("the app saw %s on Host %s", s.Path, s.Host)
	}
	want := map[string]string{"Lodge-Caller": "session", "Lodge-Login": "alice@example.com", "Lodge-Display-Name": "Alice%20Zo%C3%AB",
		"Lodge-Node-Id": "nPhone1CNTRL", "Lodge-Node-Name": "phone"}
	for k, v := range want {
		if got := s.Header.Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if id := s.Header.Get("Lodge-Session"); !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) {
		t.Errorf("Lodge-Session = %q", id)
	}
	for _, k := range []string{"Origin", "Cookie", "Authorization", "Tailscale-User-Login", "X-Forwarded-For"} {
		if v := s.Header.Get(k); v != "" {
			t.Errorf("the app received %s: %q", k, v)
		}
	}
	list := h.sessions.List()
	if len(list) != 1 || list[0].ID != s.Header.Get("Lodge-Session") || list[0].NodeName != "phone" {
		t.Errorf("sessions = %+v", list)
	}
}

// roundTrip sends a frame and reads its echo. Once it returns, the relay
// is running, so the session is registered.
func roundTrip(t *testing.T, c *websocket.Conn) {
	t.Helper()
	if err := c.Write(t.Context(), websocket.MessageText, []byte("rt")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Read(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionUpgradeChecks(t *testing.T) {
	h := newHarness(t)
	for name, c := range map[string]struct {
		hdr    map[string]string
		status int
	}{
		"no Origin":                 {map[string]string{"Origin": ""}, http.StatusForbidden},
		"another Origin":            {map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		"Sec-Fetch-Site none":       {map[string]string{"Sec-Fetch-Site": "none"}, http.StatusForbidden},
		"Sec-Fetch-Site same-site":  {map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		"Sec-Fetch-Site cross-site": {map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
	} {
		_, resp, err := h.wsDial("/app/notes/api", c.hdr)
		if err == nil || resp == nil || resp.StatusCode != c.status {
			t.Errorf("%s: %v %v, want %d before the upgrade", name, resp, err, c.status)
		}
	}
	if h.notes.count() != 0 {
		t.Error("a refused upgrade reached the app")
	}
	if _, _, err := h.wsDial("/app/notes/api", map[string]string{"Sec-Fetch-Site": "same-origin"}); err != nil {
		t.Errorf("same-origin: %v", err)
	}
	// Identity fails before the upgrade too.
	h.who.set(func(p *peer.Peer) { p.Tagged = true })
	if _, resp, err := h.wsDial("/app/notes/api", nil); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("a tagged node upgraded: %v", err)
	}
}

// A GET without an upgrade answers as the upgrade would: the failed check,
// or 426 (M4 review M2).
func TestSessionDiagnosticGET(t *testing.T) {
	h := newHarness(t)
	a := h.get("/app/notes/api", nil)
	expect(t, a, http.StatusUpgradeRequired, "upgrade required")
	if a.header.Get("Upgrade") != "websocket" {
		t.Errorf("Upgrade = %q", a.header.Get("Upgrade"))
	}
	checkBrowserHeaders(t, "426", a.header, notesPolicy, "SAMEORIGIN")
	h.who.set(func(p *peer.Peer) { p.Login = "bob@example.com" })
	expect(t, h.get("/app/notes/api", nil), http.StatusForbidden, "login bob@example.com is not allowed")
	expect(t, h.get("/app/notes/api", map[string]string{"Origin": "https://evil.example"}), http.StatusForbidden, "another origin")
	if h.notes.count() != 0 {
		t.Error("a diagnostic GET reached the app")
	}
}

func TestSessionAppDownIs502(t *testing.T) {
	h := newHarness(t)
	_, resp, err := h.wsDial("/app/plain/api", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusBadGateway {
		t.Errorf("an app without a socket: %v %v, want 502", resp, err)
	}
	h.notes.handle = func(w http.ResponseWriter, r *http.Request) bool {
		http.Error(w, "refused", http.StatusForbidden)
		return true
	}
	if _, resp, err := h.wsDial("/app/notes/api", nil); err == nil || resp.StatusCode != http.StatusBadGateway {
		t.Errorf("an app that refuses: %v, want 502", err)
	}
	if h.sessions.Count() != 0 {
		t.Error("a failed session was kept")
	}
}

func TestSessionBinaryFrameCloses1003(t *testing.T) {
	h := newHarness(t)
	c, _, err := h.wsDial("/app/notes/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Write(t.Context(), websocket.MessageBinary, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if code := closeCode(t, c); code != websocket.StatusUnsupportedData {
		t.Errorf("close code %d, want 1003", code)
	}
}

func TestSessionFrameLimit(t *testing.T) {
	h := newHarness(t)
	c, _, err := h.wsDial("/app/notes/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	at := strings.Repeat("a", MaxFrame)
	if err := c.Write(t.Context(), websocket.MessageText, []byte(at)); err != nil {
		t.Fatal(err)
	}
	if _, got, err := c.Read(t.Context()); err != nil || len(got) != MaxFrame {
		t.Fatalf("a 16 MiB frame: %d bytes, %v", len(got), err)
	}
	if err := c.Write(t.Context(), websocket.MessageText, []byte(at+"a")); err != nil {
		t.Fatal(err)
	}
	if code := closeCode(t, c); code != websocket.StatusMessageTooBig {
		t.Errorf("past 16 MiB: close code %d, want 1009", code)
	}
}

func TestSessionAppCloseClosesBrowser(t *testing.T) {
	h := newHarness(t)
	h.notes.handle = func(w http.ResponseWriter, r *http.Request) bool {
		c, err := websocket.Accept(w, r, nil)
		if err == nil {
			c.Close(4001, "vault closed")
		}
		return true
	}
	c, _, err := h.wsDial("/app/notes/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	if code := closeCode(t, c); code != 4001 {
		t.Errorf("close code %d, want the app's 4001", code)
	}
}

func TestSessionBrowserCloseClosesApp(t *testing.T) {
	h := newHarness(t)
	appClosed := make(chan websocket.StatusCode, 1)
	h.notes.handle = func(w http.ResponseWriter, r *http.Request) bool {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return true
		}
		_, _, err = c.Read(context.Background())
		appClosed <- websocket.CloseStatus(err)
		return true
	}
	c, _, err := h.wsDial("/app/notes/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Close(websocket.StatusNormalClosure, "")
	select {
	case code := <-appClosed:
		if code != websocket.StatusNormalClosure {
			t.Errorf("the app saw %d, want 1000", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the app's side stayed open")
	}
}

// The liveness rule: 90 s without a frame from the browser closes the
// session (1001); a frame resets it.
func TestSessionLiveness(t *testing.T) {
	h := newHarness(t)
	c, _, err := h.wsDial("/app/notes/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, c)
	h.clock.advance(sessions.Idle - time.Second)
	roundTrip(t, c)
	h.clock.advance(sessions.Idle - time.Second)
	if n := h.sessions.Sweep(h.clock.now()); n != 0 {
		t.Fatalf("a session seen 89 s ago was swept")
	}
	h.clock.advance(time.Second)
	if n := h.sessions.Sweep(h.clock.now()); n != 1 {
		t.Fatalf("Sweep at 90 s closed %d", n)
	}
	if code := closeCode(t, c); code != websocket.StatusGoingAway {
		t.Errorf("close code %d, want 1001", code)
	}
}

func TestSessionPerNodeEvictsOldest(t *testing.T) {
	h := newHarness(t)
	var conns []*websocket.Conn
	for i := range sessions.PerNodeApp {
		c, _, err := h.wsDial("/app/notes/api", nil)
		if err != nil {
			t.Fatalf("session %d: %v", i+1, err)
		}
		conns = append(conns, c)
		roundTrip(t, c)
		h.clock.advance(time.Second)
	}
	// The first is the oldest by seenAt until it sends a frame.
	roundTrip(t, conns[0])
	c, _, err := h.wsDial("/app/notes/api", nil)
	if err != nil {
		t.Fatalf("the ninth session: %v", err)
	}
	if code := closeCode(t, conns[1]); code != websocket.StatusPolicyViolation {
		t.Errorf("the oldest-seen session closed with %d, want 1008", code)
	}
	roundTrip(t, c)
	if n := h.sessions.Count(); n != sessions.PerNodeApp {
		t.Errorf("%d sessions, want %d", n, sessions.PerNodeApp)
	}
}

func TestSessionTotalLimitIs429(t *testing.T) {
	h := newHarness(t)
	var held []*sessions.Reservation
	for range sessions.Max - 1 {
		v, err := h.sessions.Reserve()
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, v)
	}
	// The 64th opens; the 65th is refused before the upgrade.
	if _, _, err := h.wsDial("/app/notes/api", nil); err != nil {
		t.Fatalf("the 64th session: %v", err)
	}
	_, resp, err := h.wsDial("/app/notes/api", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("the 65th session: %v %v, want 429", resp, err)
	}
	for _, v := range held {
		v.Cancel()
	}
}

func TestSessionRecheckAndEnd(t *testing.T) {
	h := newHarness(t)
	a, _, err := h.wsDial("/app/notes/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, a)
	if n := h.sessions.Recheck(t.Context()); n != 0 {
		t.Fatalf("a passing re-check closed %d", n)
	}
	// The peer at the address changes: 1008.
	h.who.set(func(p *peer.Peer) { p.NodeID = "nOther" })
	if n := h.sessions.Recheck(t.Context()); n != 1 {
		t.Fatalf("Recheck closed %d", n)
	}
	if code := closeCode(t, a); code != websocket.StatusPolicyViolation {
		t.Errorf("close code %d, want 1008", code)
	}
	h.who.set(func(p *peer.Peer) { p.NodeID = "nPhone1CNTRL" })
	b, _, err := h.wsDial("/app/notes/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, b)
	// Disallowing the login closes it at once.
	if n := h.sessions.EndLogin("alice@example.com"); n != 1 {
		t.Fatalf("EndLogin closed %d", n)
	}
	if code := closeCode(t, b); code != websocket.StatusPolicyViolation {
		t.Errorf("close code %d, want 1008", code)
	}
	// A disallowed login fails the re-check too.
	c, _, err := h.wsDial("/app/notes/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, c)
	h.allow.DisallowLogin(t.Context(), "alice@example.com")
	h.sessions.Recheck(t.Context())
	if code := closeCode(t, c); code != websocket.StatusPolicyViolation {
		t.Errorf("close code %d, want 1008", code)
	}
}

// A native client on an allowed node (D7; lodge-http.md, Native clients)
// sends lodge's Origin and no Sec-Fetch-*: it reads /lodge/apps and opens
// a session as the shell would, and its identity is still WhoIs's.
func TestNativeClient(t *testing.T) {
	h := newHarness(t)
	native := map[string]string{"Origin": lodgeOrigin}
	a := h.get("/lodge/apps", native)
	if a.status != http.StatusOK || !strings.Contains(a.body, `"contentOrigin":"https://`+contentHost+`"`) {
		t.Fatalf("/lodge/apps for a native client: %d %q", a.status, a.body)
	}
	c, _, err := h.wsDial("/app/notes/api", native)
	if err != nil {
		t.Fatalf("a native client's session: %v", err)
	}
	roundTrip(t, c)
	if s := h.notes.last(t); s.Header.Get("Lodge-Login") != "alice@example.com" || s.Header.Get("Lodge-Node-Name") != "phone" {
		t.Errorf("the app saw %v", s.Header)
	}
	// Without lodge's Origin, the upgrade fails; from a node that is not
	// allowed, both fail.
	if _, resp, err := h.wsDial("/app/notes/api", map[string]string{"Origin": ""}); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("an upgrade without Origin: %v", err)
	}
	h.who.set(func(p *peer.Peer) { p.NodeID = "nOther" })
	expect(t, h.get("/lodge/apps", native), http.StatusForbidden, "node phone (nOther) is not allowed")
	if _, resp, err := h.wsDial("/app/notes/api", native); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("a native client on a node that is not allowed: %v", err)
	}
}
