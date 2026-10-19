package agents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig(t *testing.T) {
	if names, err := LoadConfig(filepath.Join(t.TempDir(), "none.json")); err != nil || names != nil {
		t.Errorf("a missing file: %v, %v", names, err)
	}
	if names, err := LoadConfig(writeConfig(t, `{"inference": {}}`)); err != nil || names != nil {
		t.Errorf("no agents member: %v, %v", names, err)
	}
	names, err := LoadConfig(writeConfig(t, `{"agents": ["frostporter", "_build-1"]}`))
	if err != nil || len(names) != 2 || names[0] != "frostporter" {
		t.Errorf("two agents: %v, %v", names, err)
	}
	sixteen := make([]string, MaxAgents)
	for i := range sixteen {
		sixteen[i] = fmt.Sprintf(`"a%d"`, i)
	}
	if _, err := LoadConfig(writeConfig(t, `{"agents": [`+strings.Join(sixteen, ",")+`]}`)); err != nil {
		t.Errorf("%d agents: %v", MaxAgents, err)
	}
	for name, body := range map[string]string{
		"seventeen":    `{"agents": [` + strings.Join(sixteen, ",") + `, "b"]}`,
		"root":         `{"agents": ["root"]}`,
		"lodge":        `{"agents": ["lodge"]}`,
		"twice":        `{"agents": ["a", "a"]}`,
		"upper case":   `{"agents": ["Porter"]}`,
		"a slash":      `{"agents": ["a/b"]}`,
		"empty":        `{"agents": [""]}`,
		"too long":     `{"agents": ["` + strings.Repeat("a", 33) + `"]}`,
		"not an array": `{"agents": "frostporter"}`,
		"a number":     `{"agents": [1]}`,
		"not JSON":     `{"agents": [`,
	} {
		if _, err := LoadConfig(writeConfig(t, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// users resolves names to uids for tests.
func users(m map[string]int) func(string) (int, error) {
	return func(name string) (int, error) {
		if uid, ok := m[name]; ok {
			return uid, nil
		}
		return -1, errors.New("no such user")
	}
}

func TestLoadListsAndKeepsOnError(t *testing.T) {
	a := New(Options{Path: "/run/lodge/agent.sock", DevUID: -1, Lookup: users(map[string]int{"frostporter": 990})})
	a.Load([]string{"frostporter", "ghost"}, nil)
	if name, ok := a.Admit(990); !ok || name != "frostporter" {
		t.Errorf("Admit(990) = %q, %v", name, ok)
	}
	if _, ok := a.Admit(991); ok {
		t.Error("an unlisted uid is admitted")
	}
	if !a.Listed("frostporter") || a.Listed("ghost") || a.Listed("root") {
		t.Error("Listed is wrong")
	}
	st := a.Status()
	if st.Path != "/run/lodge/agent.sock" || len(st.Agents) != 1 || st.Agents[0] != "frostporter" || len(st.Skipped) != 1 || st.Skipped[0] != "ghost" || st.Refused != "" {
		t.Errorf("Status = %+v", st)
	}
	// A broken configuration keeps the list, and says why.
	a.Load(nil, errors.New("agents: root cannot be an agent"))
	if _, ok := a.Admit(990); !ok {
		t.Error("a refused configuration dropped the list")
	}
	if st := a.Status(); !strings.Contains(st.Refused, "root cannot") {
		t.Errorf("Status.Refused = %q", st.Refused)
	}
	a.Load(nil, nil)
	if _, ok := a.Admit(990); ok {
		t.Error("an empty list still admits")
	}
	if st := a.Status(); st.Refused != "" {
		t.Errorf("a good load kept Refused %q", st.Refused)
	}
}

func TestDeveloperPassesUnderRoot(t *testing.T) {
	a := New(Options{DevUID: os.Getuid(), Lookup: users(nil)})
	name, ok := a.Admit(os.Getuid())
	if !ok || name == "" {
		t.Fatalf("Admit(dev) = %q, %v", name, ok)
	}
	if !a.Listed(name) {
		t.Error("the developer cannot be issued a token")
	}
}

// socket serves an agent socket in a temp dir with a, and returns its path
// and the connections the server accepted.
func socket(t *testing.T, a *Agents) (string, <-chan net.Conn) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "agent.sock")
	ln, err := Listen(path, "")
	if err != nil {
		t.Fatal(err)
	}
	l := a.Listener(ln)
	t.Cleanup(func() { l.Close() })
	accepted := make(chan net.Conn, 128)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	return path, accepted
}

func dial(t *testing.T, path string) net.Conn {
	t.Helper()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// closedByServer reports whether the server closed c, reading until EOF.
func closedByServer(t *testing.T, c net.Conn) bool {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, err := io.ReadAll(c)
	var ne net.Error
	return !errors.As(err, &ne) || !ne.Timeout()
}

func TestListenMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := Listen(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	fi, err := os.Stat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o660 {
		t.Errorf("the socket is %v, %v; want a socket, 0660", fi.Mode(), err)
	}
	if _, err := Listen(filepath.Join(t.TempDir(), "x.sock"), "nosuchlodgegroup"); err == nil {
		t.Error("a missing group was accepted")
	}
	if _, err := Listen("/"+strings.Repeat("d", 120)+"/agent.sock", ""); err == nil || !strings.Contains(err.Error(), "107 bytes") {
		t.Errorf("a long path: %v", err)
	}
}

// The socket closes a peer that is not a listed agent before reading, and
// gives an admitted connection its agent's user.
func TestTheSocketAdmitsOnlyListedAgents(t *testing.T) {
	a := New(Options{DevUID: -1, Lookup: users(map[string]int{"frostporter": os.Getuid()})})
	path, accepted := socket(t, a)
	if !closedByServer(t, dial(t, path)) {
		t.Fatal("an unlisted peer was kept")
	}
	select {
	case <-accepted:
		t.Fatal("an unlisted peer was accepted")
	default:
	}
	a.Load([]string{"frostporter"}, nil)
	c := dial(t, path)
	var sc net.Conn
	select {
	case sc = <-accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("a listed agent was not accepted")
	}
	if user, ok := User(ConnContext(t.Context(), sc)); !ok || user != "frostporter" {
		t.Errorf("User = %q, %v", user, ok)
	}
	if _, ok := User(ConnContext(t.Context(), c)); ok {
		t.Error("a connection not from the socket has a user")
	}
	// Reloading without the agent closes its connections.
	a.Load(nil, nil)
	if !closedByServer(t, c) {
		t.Error("a removed agent's connection stayed open")
	}
	// A reload that keeps it keeps them.
	a.Load([]string{"frostporter"}, nil)
	c2 := dial(t, path)
	<-accepted
	a.Load([]string{"frostporter"}, nil)
	_ = c2.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := c2.Read(make([]byte, 1)); !isTimeout(err) {
		t.Errorf("a kept agent's connection: %v", err)
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// The socket holds at most PerUID connections from one uid, and Total in
// all; a closed one frees its place.
func TestTheSocketCapsConnections(t *testing.T) {
	a := New(Options{DevUID: -1, PerUID: 3, Total: 100, Lookup: users(map[string]int{"frostporter": os.Getuid()})})
	a.Load([]string{"frostporter"}, nil)
	path, accepted := socket(t, a)
	var held []net.Conn
	for range 3 {
		dial(t, path)
		held = append(held, <-accepted)
	}
	if !closedByServer(t, dial(t, path)) {
		t.Fatal("a fourth connection was kept")
	}
	held[0].Close()
	dial(t, path)
	select {
	case <-accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("a closed connection did not free its place")
	}
}

// counter counts log records.
type counter struct {
	mu sync.Mutex
	n  int
}

func (h *counter) Enabled(context.Context, slog.Level) bool { return true }
func (h *counter) Handle(context.Context, slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.n++
	return nil
}
func (h *counter) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *counter) WithGroup(string) slog.Handler      { return h }

func (h *counter) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.n
}

func TestRefusalsAreLoggedOncePerMinute(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	logs := &counter{}
	a := New(Options{DevUID: -1, Lookup: users(nil), Log: slog.New(logs), Now: func() time.Time { return now }})
	for range 3 {
		if _, ok := a.accept(nil, 4242); ok {
			t.Fatal("an unlisted uid was accepted")
		}
	}
	if logs.count() != 1 {
		t.Errorf("three refusals in a minute logged %d lines, want 1", logs.count())
	}
	now = now.Add(RefusalLogEvery)
	a.accept(nil, 4242)
	if logs.count() != 2 {
		t.Errorf("a refusal a minute later: %d lines, want 2", logs.count())
	}
}
