package gateway

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// acceptAll serves ln, handing each accepted connection to the test.
func acceptAll(t *testing.T, ln net.Listener) <-chan net.Conn {
	t.Helper()
	ch := make(chan net.Conn, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				close(ch)
				return
			}
			ch <- c
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ch
}

func dialFrom(t *testing.T, addr, from string) net.Conn {
	t.Helper()
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(from)}, Timeout: 5 * time.Second}
	c, err := d.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// refused reports whether lodge closed c without reading from it.
func refused(t *testing.T, c net.Conn) bool {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := c.Read(make([]byte, 1))
	return errors.Is(err, io.EOF) || strings.Contains(fmtErr(err), "reset")
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestLimitPerPeer(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := acceptAll(t, Limit(inner, 2, 10))
	addr := inner.Addr().String()
	var held []net.Conn
	for range 2 {
		dialFrom(t, addr, "127.0.0.1")
		held = append(held, <-accepted)
	}
	// At the limit both are held; past it the third is closed at once.
	if c := dialFrom(t, addr, "127.0.0.1"); !refused(t, c) {
		t.Fatal("a third connection from one address was kept")
	}
	// Another address has its own count.
	dialFrom(t, addr, "127.0.0.2")
	<-accepted
	// Closing one frees its place.
	held[0].Close()
	dialFrom(t, addr, "127.0.0.1")
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("a freed place was not reused")
	}
}

func TestLimitTotal(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := acceptAll(t, Limit(inner, 10, 3))
	addr := inner.Addr().String()
	for i, from := range []string{"127.0.0.1", "127.0.0.2", "127.0.0.3"} {
		dialFrom(t, addr, from)
		select {
		case <-accepted:
		case <-time.After(5 * time.Second):
			t.Fatalf("connection %d at the limit was refused", i+1)
		}
	}
	if c := dialFrom(t, addr, "127.0.0.4"); !refused(t, c) {
		t.Fatal("a connection past the total was kept")
	}
}

// A refused request does not keep its connection (M2).
func TestRefusalsCloseTheConnection(t *testing.T) {
	h := newHarness(t)
	secret := h.issue("agent", "notes", "vault=1")
	for name, r := range map[string]*http.Request{
		"401":        h.mcpReq(http.MethodPost, "notes", secret+"x", nil),
		"405":        h.req(http.MethodPost, "/app/notes/", nil),
		"403":        h.req(http.MethodGet, "/app/notes/", map[string]string{"Origin": "https://evil.example"}),
		"426":        h.req(http.MethodGet, "/app/notes/api", nil),
		"x/ upgrade": h.req(http.MethodGet, "/app/notes/x/a", map[string]string{"Connection": "Upgrade", "Upgrade": "websocket"}),
	} {
		r.Close = false
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if !resp.Close {
			t.Errorf("%s: the connection was kept", name)
		}
	}
	// A request that passes keeps it.
	r := h.req(http.MethodGet, "/app/notes/", nil)
	r.Close = false
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Close {
		t.Error("an admitted request closed its connection")
	}
}

func TestHeaderLimit(t *testing.T) {
	h := newHarness(t)
	at := h.get("/app/notes/", map[string]string{"X-Big": strings.Repeat("a", MaxHeaderBytes/2)})
	if at.status != http.StatusOK {
		t.Errorf("a header inside the limit: %d", at.status)
	}
	past := h.get("/app/notes/", map[string]string{"X-Big": strings.Repeat("a", 2*MaxHeaderBytes)})
	if past.status != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("a header past the limit: %d, want 431", past.status)
	}
	if h.g.transport.MaxConnsPerHost != MaxConnsPerApp {
		t.Errorf("the app transport holds up to %d connections, want %d", h.g.transport.MaxConnsPerHost, MaxConnsPerApp)
	}
}

// A device request's body may be slow, but not stalled: each read has its
// own deadline (M2).
func TestDeviceBodyDeadline(t *testing.T) {
	h := newHarness(t)
	h.g.cfg.BodyTimeout = 300 * time.Millisecond
	got := make(chan error, 1)
	h.notes.handle = func(w http.ResponseWriter, r *http.Request) bool {
		b, err := io.ReadAll(r.Body)
		if err == nil && len(b) != 10 {
			err = errors.New("short body")
		}
		got <- err
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	send := func(chunks []string, gap time.Duration, length int) {
		t.Helper()
		c := dialFrom(t, strings.TrimPrefix(h.lodge.URL, "http://"), "127.0.0.1")
		w := bufio.NewWriter(c)
		w.WriteString("POST /device/notes/sync/pair HTTP/1.1\r\nHost: " + lodgeHost + "\r\n")
		w.WriteString("Content-Length: " + string(rune('0'+length/10)) + string(rune('0'+length%10)) + "\r\n\r\n")
		w.Flush()
		for _, ch := range chunks {
			time.Sleep(gap) // the body's pace is what is under test
			c.Write([]byte(ch))
		}
	}
	// Steady: five reads, each well inside the deadline.
	go send([]string{"aa", "bb", "cc", "dd", "ee"}, 100*time.Millisecond, 10)
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("a slow, steady body: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the steady body never arrived")
	}
	// Stalled: half the body, then nothing.
	go send([]string{"aaaaa"}, 0, 10)
	select {
	case err := <-got:
		if err == nil {
			t.Fatal("a stalled body was read in full")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a stalled body held the app for 10 s")
	}
}
