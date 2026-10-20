package gateway

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frostyard/lodge/internal/agents"
)

// agentSocket is the agent socket, served by the harness's gateway, with
// this process's uid listed as frostporter.
type agentSocket struct {
	h      *harness
	agents *agents.Agents
	client *http.Client
}

func (h *harness) agentSocket() *agentSocket {
	h.t.Helper()
	uid := os.Getuid()
	ag := agents.New(agents.Options{DevUID: -1, Lookup: func(name string) (int, error) {
		switch name {
		case "frostporter":
			return uid, nil
		case "frostother":
			return uid + 1, nil
		}
		return -1, errors.New("no such user")
	}})
	ag.Load([]string{"frostporter", "frostother"}, nil)
	dir, err := os.MkdirTemp("", "ags")
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "agent.sock")
	ln, err := agents.Listen(path, "")
	if err != nil {
		h.t.Fatal(err)
	}
	srv := Server(h.g.Agent())
	perConn := srv.ConnContext
	srv.ConnContext = func(ctx context.Context, c net.Conn) context.Context { return agents.ConnContext(perConn(ctx, c), c) }
	go srv.Serve(ag.Listener(ln))
	h.t.Cleanup(func() { srv.Close() })
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}, DisableKeepAlives: true}
	h.t.Cleanup(tr.CloseIdleConnections)
	return &agentSocket{h: h, agents: ag, client: &http.Client{Transport: tr}}
}

// issueFor issues a token bound to agent.
func (h *harness) issueFor(name, app, scope, agent string) string {
	h.t.Helper()
	secret, err := h.tokens.Issue(h.t.Context(), name, app, scope, agent)
	if err != nil {
		h.t.Fatal(err)
	}
	return secret
}

func (s *agentSocket) req(method, path, secret string, hdr map[string]string) *http.Request {
	s.h.t.Helper()
	r, err := http.NewRequestWithContext(s.h.t.Context(), method, "http://agent"+path, nil)
	if err != nil {
		s.h.t.Fatal(err)
	}
	if secret != "" {
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func (s *agentSocket) do(r *http.Request) answer {
	s.h.t.Helper()
	resp, err := s.client.Do(r)
	if err != nil {
		s.h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return answer{resp.StatusCode, resp.Header, string(b)}
}

func TestAgentSocketChecks(t *testing.T) {
	h := newHarness(t)
	s := h.agentSocket()
	secret := h.issueFor("porter", "notes", "vault=1", "frostporter")
	// No Host check: any Host reaches the token check.
	for _, host := range []string{"agent", "evil.example", lodgeHost, ""} {
		r := s.req(http.MethodPost, "/mcp/notes", secret, nil)
		r.Host = host
		if a := s.do(r); a.status != http.StatusOK {
			t.Errorf("Host %q: %d %q", host, a.status, a.body)
		}
	}
	expect(t, s.do(s.req(http.MethodPost, "/mcp/notes", secret, map[string]string{"Origin": lodgeOrigin})), http.StatusForbidden, "browsers")
	for _, m := range []string{http.MethodPut, http.MethodPatch, http.MethodHead} {
		a := s.do(s.req(m, "/mcp/notes", secret, nil))
		if a.status != http.StatusMethodNotAllowed || a.header.Get("Allow") != "POST, GET, DELETE" {
			t.Errorf("%s: %d Allow %q", m, a.status, a.header.Get("Allow"))
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		if a := s.do(s.req(m, "/mcp/notes", secret, nil)); a.status != http.StatusOK {
			t.Errorf("%s: %d %q", m, a.status, a.body)
		}
	}
	// /mcp/<id> alone: nothing else of lodge's is on the socket.
	for _, p := range []string{"/", "/mcp", "/mcp/", "/mcp/notes/x", "/notes", "/app/notes/", "/app/notes/api", "/lodge/apps",
		"/device/notes/sync", "/mcp/%6Eotes/..", "/mcp/Notes"} {
		if a := s.do(s.req(http.MethodGet, p, secret, nil)); a.status != http.StatusNotFound {
			t.Errorf("%s: %d %q", p, a.status, a.body)
		}
	}
	expect(t, s.do(s.req(http.MethodGet, "/mcp/notes", secret, map[string]string{"Connection": "Upgrade", "Upgrade": "websocket"})),
		http.StatusBadRequest, "upgrade")
}

func TestAgentSocketTokens(t *testing.T) {
	h := newHarness(t)
	s := h.agentSocket()
	tailnet := h.issue("claude-code@laptop", "notes", "vault=1")
	other := h.issueFor("other", "notes", "vault=1", "frostother")
	mail := h.issueFor("porter-mail", "mail", "x", "frostporter")
	revoked := h.issueFor("old", "notes", "x", "frostporter")
	h.tokens.Revoke(t.Context(), "old")
	for name, auth := range map[string]string{
		"missing": "", "malformed": "Bearer lgm_nope", "unknown": "Bearer lgm_" + strings.Repeat("A", 43),
		"revoked": "Bearer " + revoked, "another app": "Bearer " + mail,
		"the tailnet's": "Bearer " + tailnet, "another agent's": "Bearer " + other,
	} {
		r := s.req(http.MethodPost, "/mcp/notes", "", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		a := s.do(r)
		if a.status != http.StatusUnauthorized || a.header.Get("WWW-Authenticate") != "Bearer" || strings.TrimSpace(a.body) != "unauthorized" {
			t.Errorf("%s token: %d %q %q", name, a.status, a.header.Get("WWW-Authenticate"), a.body)
		}
	}
	if h.notes.count() != 0 {
		t.Error("a refused token reached the app")
	}
	// The app: no mcp is 404, disabled is 503.
	plain := h.issueFor("p", "plain", "x", "frostporter")
	expect(t, s.do(s.req(http.MethodPost, "/mcp/plain", plain, nil)), http.StatusNotFound, "")
	off := h.issueFor("o", "off", "x", "frostporter")
	expect(t, s.do(s.req(http.MethodPost, "/mcp/off", off, nil)), http.StatusServiceUnavailable, "disabled")
}

// A token issued for the agent socket is refused on the tailnet.
func TestTheTailnetRefusesAgentTokens(t *testing.T) {
	h := newHarness(t)
	secret := h.issueFor("porter", "notes", "vault=1", "frostporter")
	a := h.do(h.mcpReq(http.MethodPost, "notes", secret, nil))
	if a.status != http.StatusUnauthorized || a.header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("an agent's token on the tailnet: %d %q", a.status, a.body)
	}
	if h.notes.count() != 0 {
		t.Error("the app was reached")
	}
}

// records keeps log records' attributes, for the MCP log line.
type records struct {
	mu      sync.Mutex
	recs    []map[string]string
	changed chan struct{} // closed at the next record, for await
}

func (r *records) Enabled(context.Context, slog.Level) bool { return true }
func (r *records) Handle(_ context.Context, rec slog.Record) error {
	m := map[string]string{"msg": rec.Message}
	rec.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.String(); return true })
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, m)
	if r.changed != nil {
		close(r.changed)
		r.changed = nil
	}
	return nil
}
func (r *records) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *records) WithGroup(string) slog.Handler      { return r }

func (r *records) find(msg string) []map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.findLocked(msg)
}

func (r *records) findLocked(msg string) []map[string]string {
	var out []map[string]string
	for _, m := range r.recs {
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

// await returns msg's records once there are n of them, or those there
// are after 5 seconds. The MCP line is logged once the response is done,
// which can be after the client has read it.
func (r *records) await(msg string, n int) []map[string]string {
	deadline := time.After(5 * time.Second)
	for {
		r.mu.Lock()
		out := r.findLocked(msg)
		if len(out) >= n {
			r.mu.Unlock()
			return out
		}
		if r.changed == nil {
			r.changed = make(chan struct{})
		}
		next := r.changed
		r.mu.Unlock()
		select {
		case <-next:
		case <-deadline:
			return r.find(msg)
		}
	}
}

func TestAgentSocketProxy(t *testing.T) {
	h := newHarness(t)
	logs := &records{}
	h.g.log = slog.New(logs)
	s := h.agentSocket()
	secret := h.issueFor("porter", "notes", "vault=1;write=Porter/", "frostporter")
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
	a := s.do(s.req(http.MethodPost, "/mcp/notes?x=1", secret, hdr))
	if a.status != http.StatusOK || a.header.Get("Mcp-Session-Id") != "s-123" {
		t.Fatalf("mcp: %d %v", a.status, a.header)
	}
	if csp := a.header.Values("Content-Security-Policy"); a.header.Get("X-Frame-Options") != "DENY" || len(csp) == 0 ||
		csp[len(csp)-1] != "default-src 'none'; sandbox; frame-ancestors 'none'" {
		t.Errorf("the response is not confined: %v", a.header)
	}
	seen := h.notes.last(t)
	if seen.Path != "/mcp" || seen.Method != http.MethodPost || seen.Query != "x=1" || seen.Host != "notes" {
		t.Errorf("the app saw %+v", seen)
	}
	for k, v := range map[string]string{"Lodge-Caller": "agent", "Lodge-Token": "porter", "Lodge-Scope": "vault=1;write=Porter/",
		"Mcp-Session-Id": "s-123", "Mcp-Protocol-Version": "2025-06-18"} {
		if got := seen.Header.Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if seen.Header.Get("Authorization") != "" {
		t.Error("the app received the token")
	}
	for _, k := range []string{"Lodge-Login", "Lodge-Node-Id", "Lodge-Node-Name", "Lodge-Session"} {
		if seen.Header.Get(k) != "" {
			t.Errorf("an agent request carries %s", k)
		}
	}
	checkNoForged(t, "agent socket", seen.Header, "Lodge-Caller", "Lodge-Token", "Lodge-Scope")
	list, _ := h.tokens.List(t.Context())
	if list[0].LastUsed.IsZero() {
		t.Error("the token's use was not recorded")
	}
	// The log names the agent's user in place of a node.
	lines := logs.await("mcp", 1)
	if len(lines) != 1 || lines[0]["agent"] != "frostporter" || lines[0]["token"] != "porter" || lines[0]["status"] != "200" {
		t.Fatalf("the log: %v", lines)
	}
	if _, ok := lines[0]["node"]; ok {
		t.Error("the log names a node")
	}
}

// Streams are relayed without buffering, and revoking the token closes
// them at once.
func TestAgentSocketStreamAndRevocation(t *testing.T) {
	h := newHarness(t)
	s := h.agentSocket()
	secret := h.issueFor("porter", "notes", "vault=1", "frostporter")
	appDone := make(chan struct{})
	h.notes.handle = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(appDone)
		return true
	}
	resp, err := s.client.Do(s.req(http.MethodGet, "/mcp/notes", secret, map[string]string{"Accept": "text/event-stream"}))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("the first event arrived as %q, %v", line, err)
	}
	id, err := h.tokens.Revoke(t.Context(), "porter")
	if err != nil {
		t.Fatal(err)
	}
	if n := h.g.EndToken(id); n != 1 {
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
	expect(t, s.do(s.req(http.MethodPost, "/mcp/notes", secret, nil)), http.StatusUnauthorized, "")
}

// A peer the agents do not list is closed before it is read, and removing
// an agent closes its streams.
func TestAgentSocketPeers(t *testing.T) {
	h := newHarness(t)
	s := h.agentSocket()
	secret := h.issueFor("porter", "notes", "vault=1", "frostporter")
	appDone := make(chan struct{})
	h.notes.handle = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(appDone)
		return true
	}
	resp, err := s.client.Do(s.req(http.MethodGet, "/mcp/notes", secret, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	s.agents.Load([]string{"frostother"}, nil)
	select {
	case <-appDone:
	case <-time.After(10 * time.Second):
		t.Fatal("a removed agent's stream stayed open")
	}
	if _, err := s.client.Do(s.req(http.MethodPost, "/mcp/notes", secret, nil)); err == nil {
		t.Error("a peer that is no longer an agent got an answer")
	}
}
