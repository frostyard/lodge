package control

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/lodge/internal/allow"
	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/apptest"
	"github.com/frostyard/lodge/internal/inference"
	"github.com/frostyard/lodge/internal/peer"
	"github.com/frostyard/lodge/internal/sessions"
	"github.com/frostyard/lodge/internal/store"
	"github.com/frostyard/lodge/internal/tokens"
)

type fixture struct {
	c        *Client
	allow    *allow.Lists
	tokens   *tokens.Store
	sessions *sessions.Registry
	ended    []int64
	reloads  int
	sock     string
}

func newFixture(t *testing.T, peers []peer.Peer) *fixture {
	t.Helper()
	return newFixtureWith(t, peers, nil)
}

func newFixtureWith(t *testing.T, peers []peer.Peer, edit func(*Deps)) *fixture {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(root, "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	apptest.Install(t, root, "notes", map[string]any{"mcp": true}, nil)
	apptest.Install(t, root, "plain", nil, nil)
	reg := apps.New(root, nil)
	reg.Load()
	f := &fixture{allow: allow.New(db, nil), tokens: tokens.New(db, nil), sessions: sessions.NewRegistry()}
	deps := Deps{
		Allow: f.allow, Tokens: f.tokens, Sessions: f.sessions, Apps: reg,
		EndToken: func(id int64) int { f.ended = append(f.ended, id); return 2 },
		Peers:    func(context.Context) ([]peer.Peer, error) { return peers, nil },
		Nodes:    func(context.Context) []Node { return []Node{{Name: "lodge", DNSName: "lodge.example", Up: true}} },
		Reload:   func() error { f.reloads++; return reg.Load() },
		MCPURL:   func(id string) string { return "https://lodge.example/mcp/" + id },
	}
	if edit != nil {
		edit(&deps)
	}
	srv := New(deps)
	f.sock = filepath.Join(root, "run/lodge/control.sock")
	ln, err := Listen(f.sock)
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
	f.c, err = Dial(t.Context(), f.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.c.Close() })
	return f
}

func code(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

func TestSocketMode(t *testing.T) {
	f := newFixture(t, nil)
	fi, err := os.Stat(f.sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("control socket mode %v, want a 0600 socket", fi.Mode())
	}
}

func TestAdmit(t *testing.T) {
	s := New(Deps{})
	if !s.admit(0) || !s.admit(os.Getuid()) {
		t.Error("root or lodge's own uid is refused")
	}
	for _, uid := range []int{os.Getuid() + 1, 65534, 1000 + os.Getuid()%7 + 12345} {
		if uid != os.Getuid() && uid != 0 && s.admit(uid) {
			t.Errorf("uid %d is admitted", uid)
		}
	}
}

func TestLogins(t *testing.T) {
	f := newFixture(t, nil)
	ctx := t.Context()
	if err := f.c.Call(ctx, "lodge.allowLogin", map[string]string{"login": "alice@example.com"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Call(ctx, "lodge.allowLogin", map[string]string{"login": "alice@example.com"}, nil); code(err) != CodeConflict {
		t.Errorf("allowing twice: %v, want conflict", err)
	}
	if err := f.c.Call(ctx, "lodge.allowLogin", map[string]string{"login": "a\nb"}, nil); code(err) != CodeInvalidParams {
		t.Errorf("a bad login: %v, want invalidParams", err)
	}
	var logins []allow.Login
	if err := f.c.Call(ctx, "lodge.logins", nil, &logins); err != nil || len(logins) != 1 || logins[0].Login != "alice@example.com" {
		t.Errorf("logins = %+v, %v", logins, err)
	}
	// Disallowing closes the login's sessions.
	closed := 0
	s := sessions.New("notes", peer.Peer{Login: "alice@example.com", NodeID: "n1"}, "", time.Now(), func(int, string) { closed++ })
	v, _ := f.sessions.Reserve()
	v.Add(s)
	var out Closed
	if err := f.c.Call(ctx, "lodge.disallowLogin", map[string]string{"login": "alice@example.com"}, &out); err != nil || out.Closed != 1 || closed != 1 {
		t.Errorf("disallow: %+v %v, closed %d", out, err, closed)
	}
	if err := f.c.Call(ctx, "lodge.disallowLogin", map[string]string{"login": "alice@example.com"}, nil); code(err) != CodeNotFound {
		t.Errorf("disallowing an absent login: %v, want notFound", err)
	}
}

func TestNodes(t *testing.T) {
	peers := []peer.Peer{{NodeID: "nPhone", NodeName: "phone"}, {NodeID: "nTwin1", NodeName: "twin"}, {NodeID: "nTwin2", NodeName: "twin"}}
	f := newFixture(t, peers)
	ctx := t.Context()
	var got AllowedNode
	if err := f.c.Call(ctx, "lodge.allowNode", map[string]string{"node": "phone"}, &got); err != nil || got != (AllowedNode{"nPhone", "phone"}) {
		t.Errorf("allow by name: %+v %v", got, err)
	}
	if err := f.c.Call(ctx, "lodge.allowNode", map[string]string{"node": "nPhone"}, nil); code(err) != CodeConflict {
		t.Errorf("allowing twice: %v", err)
	}
	if err := f.c.Call(ctx, "lodge.allowNode", map[string]string{"node": "twin"}, nil); code(err) != CodeInvalidParams {
		t.Errorf("an ambiguous name: %v, want invalidParams", err)
	}
	if err := f.c.Call(ctx, "lodge.allowNode", map[string]string{"node": "no such node"}, nil); code(err) != CodeNotFound {
		t.Errorf("an unknown name: %v, want notFound", err)
	}
	if err := f.c.Call(ctx, "lodge.allowNode", map[string]string{"node": "nTwin2"}, &got); err != nil || got.Name != "twin" {
		t.Errorf("allow by ID: %+v %v", got, err)
	}
	if err := f.c.Call(ctx, "lodge.allowNode", map[string]string{"node": "nOffline"}, &got); err != nil || got.ID != "nOffline" {
		t.Errorf("an ID not among the peers: %+v %v", got, err)
	}
	var nodes []allow.Node
	f.c.Call(ctx, "lodge.nodes", nil, &nodes)
	if len(nodes) != 3 {
		t.Errorf("nodes = %+v", nodes)
	}
	closed := 0
	v, _ := f.sessions.Reserve()
	v.Add(sessions.New("notes", peer.Peer{NodeID: "nPhone"}, "", time.Now(), func(int, string) { closed++ }))
	var out Closed
	if err := f.c.Call(ctx, "lodge.disallowNode", map[string]string{"node": "phone"}, &out); err != nil || out.Closed != 1 || closed != 1 {
		t.Errorf("disallow by name: %+v %v", out, err)
	}
	if err := f.c.Call(ctx, "lodge.disallowNode", map[string]string{"node": "nPhone"}, nil); code(err) != CodeNotFound {
		t.Errorf("disallowing twice: %v", err)
	}
}

func TestTokens(t *testing.T) {
	f := newFixture(t, nil)
	ctx := t.Context()
	var out Issued
	if err := f.c.Call(ctx, "lodge.issueToken", map[string]string{"name": "agent", "app": "notes", "scope": "vault=1"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Token, "lgm_") || out.URL != "https://lodge.example/mcp/notes" {
		t.Errorf("issued %+v", out)
	}
	if _, err := f.tokens.Check(ctx, out.Token, "notes"); err != nil {
		t.Errorf("the issued token does not check: %v", err)
	}
	for name, c := range map[string]struct {
		p    map[string]string
		code int
	}{
		"live name":   {map[string]string{"name": "agent", "app": "notes", "scope": "x"}, CodeConflict},
		"no MCP":      {map[string]string{"name": "b", "app": "plain", "scope": "x"}, CodeNotFound},
		"no app":      {map[string]string{"name": "b", "app": "nosuch", "scope": "x"}, CodeNotFound},
		"bad name":    {map[string]string{"name": "a b", "app": "notes", "scope": "x"}, CodeInvalidParams},
		"bad scope":   {map[string]string{"name": "b", "app": "notes", "scope": ""}, CodeInvalidParams},
		"extra param": {map[string]string{"name": "b", "app": "notes", "scope": "x", "admin": "yes"}, CodeInvalidParams},
	} {
		if err := f.c.Call(ctx, "lodge.issueToken", c.p, nil); code(err) != c.code {
			t.Errorf("%s: %v, want %d", name, err, c.code)
		}
	}
	var list []tokens.Token
	if err := f.c.Call(ctx, "lodge.tokens", nil, &list); err != nil || len(list) != 1 || list[0].Name != "agent" {
		t.Errorf("tokens = %+v %v", list, err)
	}
	var closed Closed
	if err := f.c.Call(ctx, "lodge.revokeToken", map[string]string{"name": "agent"}, &closed); err != nil || closed.Closed != 2 {
		t.Errorf("revoke: %+v %v", closed, err)
	}
	if len(f.ended) != 1 || f.ended[0] == 0 {
		t.Errorf("streams ended for %v", f.ended)
	}
	if err := f.c.Call(ctx, "lodge.revokeToken", map[string]string{"name": "agent"}, nil); code(err) != CodeNotFound {
		t.Errorf("revoking twice: %v", err)
	}
}

func TestStatusAppsSessionsReload(t *testing.T) {
	f := newFixture(t, nil)
	ctx := t.Context()
	var st Status
	if err := f.c.Call(ctx, "lodge.status", nil, &st); err != nil || len(st.Nodes) != 1 || len(st.Apps) != 2 {
		t.Errorf("status = %+v %v", st, err)
	}
	var list []apps.App
	if err := f.c.Call(ctx, "lodge.reload", nil, &list); err != nil || len(list) != 2 || f.reloads != 1 {
		t.Errorf("reload = %d apps, %v, %d reloads", len(list), err, f.reloads)
	}
	if err := f.c.Call(ctx, "lodge.apps", nil, &list); err != nil || list[0].ID == "" {
		t.Errorf("apps = %+v %v", list, err)
	}
	s := sessions.New("notes", peer.Peer{NodeID: "n1", NodeName: "phone"}, "", time.Now(), func(int, string) {})
	v, _ := f.sessions.Reserve()
	v.Add(s)
	var infos []sessions.Info
	if err := f.c.Call(ctx, "lodge.sessions", nil, &infos); err != nil || len(infos) != 1 || infos[0].NodeName != "phone" {
		t.Errorf("sessions = %+v %v", infos, err)
	}
	if err := f.c.Call(ctx, "lodge.endSession", map[string]string{"id": s.ID}, nil); err != nil {
		t.Error(err)
	}
	if err := f.c.Call(ctx, "lodge.endSession", map[string]string{"id": s.ID}, nil); code(err) != CodeNotFound {
		t.Errorf("ending twice: %v", err)
	}
	if err := f.c.Call(ctx, "lodge.nope", nil, nil); code(err) != CodeMethodNotFound {
		t.Errorf("an unknown method: %v", err)
	}
	var sums []inference.Sum
	if err := f.c.Call(ctx, "lodge.usage", nil, &sums); err != nil || len(sums) != 0 {
		t.Errorf("lodge.usage without inference: %+v %v", sums, err)
	}
}

func TestMalformedRequests(t *testing.T) {
	f := newFixture(t, nil)
	c, err := net.Dial("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	buf := make([]byte, 4096)
	for line, want := range map[string]string{
		"not json\n": `"code":-32700`,
		`{"jsonrpc":"1.0","id":1,"method":"lodge.status"}` + "\n":                  `"code":-32600`,
		`{"jsonrpc":"2.0","id":2}` + "\n":                                          `"code":-32600`,
		`{"jsonrpc":"2.0","id":3,"method":"lodge.status","params":{"x":1}}` + "\n": `"code":-32602`,
	} {
		c.Write([]byte(line))
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := c.Read(buf)
		if err != nil || !strings.Contains(string(buf[:n]), want) {
			t.Errorf("%q: %q %v, want %s", line, buf[:n], err, want)
		}
	}
}

type fakeInference struct {
	since time.Time
	keys  map[string]string
}

func (f *fakeInference) Status() ([]inference.ProviderStatus, string) {
	return []inference.ProviderStatus{{Name: "home", Type: "openai", Available: true, Slots: 4}}, "a refusal"
}

func (f *fakeInference) Usage(_ context.Context, since time.Time) ([]inference.Sum, error) {
	f.since = since
	return []inference.Sum{{App: "mail", Alias: "local", Requests: 2, Input: 10, Output: 4}}, nil
}

func (f *fakeInference) SetKey(_ context.Context, provider, key string) error {
	if provider != "home" {
		return inference.ErrNoProvider
	}
	f.keys[provider] = key
	return nil
}

func TestInferenceMethods(t *testing.T) {
	inf := &fakeInference{keys: map[string]string{}}
	f := newFixtureWith(t, nil, func(d *Deps) { d.Inference = inf })
	ctx := t.Context()
	var st Status
	if err := f.c.Call(ctx, "lodge.status", nil, &st); err != nil || len(st.Providers) != 1 || st.InferenceRefused != "a refusal" {
		t.Errorf("status = %+v %v", st, err)
	}
	var sums []inference.Sum
	if err := f.c.Call(ctx, "lodge.usage", map[string]string{"since": "2026-10-01"}, &sums); err != nil || len(sums) != 1 {
		t.Errorf("usage = %+v %v", sums, err)
	}
	if !inf.since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("since = %v", inf.since)
	}
	if err := f.c.Call(ctx, "lodge.usage", map[string]string{"since": "yesterday"}, nil); code(err) != CodeInvalidParams {
		t.Errorf("a bad since: %v", err)
	}
	if err := f.c.Call(ctx, "lodge.providerKey", map[string]string{"provider": "home", "key": "sk-1"}, nil); err != nil || inf.keys["home"] != "sk-1" {
		t.Errorf("set a key: %v %v", err, inf.keys)
	}
	if err := f.c.Call(ctx, "lodge.providerKey", map[string]string{"provider": "home", "key": ""}, nil); err != nil || inf.keys["home"] != "" {
		t.Errorf("remove a key: %v %v", err, inf.keys)
	}
	if err := f.c.Call(ctx, "lodge.providerKey", map[string]string{"provider": "nosuch", "key": "k"}, nil); code(err) != CodeNotFound {
		t.Errorf("an unknown provider: %v", err)
	}
	if err := f.c.Call(ctx, "lodge.providerKey", map[string]string{"provider": "home", "key": "two words"}, nil); code(err) != CodeInvalidParams {
		t.Errorf("a key with a space: %v", err)
	}
}
