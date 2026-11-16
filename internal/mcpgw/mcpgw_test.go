package mcpgw

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/oauth"
)

// fakeApps are the installed apps of a test.
type fakeApps struct{ list []apps.App }

func (f fakeApps) List() []apps.App { return f.list }

func (f fakeApps) Get(id string) (apps.App, bool) {
	for _, a := range f.list {
		if a.ID == id {
			return a, true
		}
	}
	return apps.App{}, false
}

// fakeOAuth answers the grants of a test, by principal and app.
type fakeOAuth struct{ grants map[string][]oauth.Item }

func key(principal, app string) string { return principal + "/" + app }

func (f *fakeOAuth) Grant(_ context.Context, principal, app string) ([]oauth.Item, error) {
	return f.grants[key(principal, app)], nil
}

func (f *fakeOAuth) RequestAccess(context.Context, string, string, []oauth.Item) (oauth.Request, error) {
	return oauth.Request{}, nil
}

func (f *fakeOAuth) Live(context.Context, string) (bool, error) { return true, nil }

// capture collects the log records of a test.
type capture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *capture) Enabled(context.Context, slog.Level) bool { return true }

func (c *capture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r.Clone())
	return nil
}

func (c *capture) WithAttrs([]slog.Attr) slog.Handler { return c }

func (c *capture) WithGroup(string) slog.Handler { return c }

// lines returns the records whose message is message, with their attributes.
func (c *capture) lines(message string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, r := range c.records {
		if r.Message != message {
			continue
		}
		attrs := map[string]any{}
		r.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.Any()
			return true
		})
		out = append(out, attrs)
	}
	return out
}

// notesApp is an app on hearth with 2 agent operations: 1 read and 1 write.
func notesApp() apps.App {
	return apps.App{ID: "notelite", State: apps.Up, Manifest: apps.Manifest{ID: "notelite", Name: "Notes Lite"},
		Hearth: &apps.Hearth{
			Capabilities: []string{"notelite:read", "notelite:write"},
			Selectors:    []string{"folder"},
			Operations: []apps.Operation{
				{Name: "example.notes.v1.NoteService.GetNote", Permission: "notelite:read", Effect: "read",
					Expose: []string{"ui", "agent"}, Tool: "get_note"},
				{Name: "example.notes.v1.NoteService.CreateNote", Permission: "notelite:write", Effect: "write",
					Expose: []string{"ui", "agent"}, Tool: "create_note"},
			},
			Tools: []apps.Tool{
				{Name: "get_note", Description: "Read a note.", InputSchema: []byte(`{"type":"object"}`)},
				{Name: "create_note", Description: "Make a note.", InputSchema: []byte(`{"type":"object"}`)},
			},
		}}
}

// TestSyncLogsToolsChanged checks the line lodge logs when a principal's tools
// change: the message, and the attributes an end-to-end test reads.
func TestSyncLogsToolsChanged(t *testing.T) {
	c := &capture{}
	o := &fakeOAuth{grants: map[string][]oauth.Item{key("chani", "notelite"): {{Cap: "notelite:read"}}}}
	g := New(Options{Apps: fakeApps{[]apps.App{notesApp()}}, OAuth: o, Log: slog.New(c)})
	// The first sync of a new server adds the tools a read's grant holds, and
	// lodge's own tool.
	g.serverFor(t.Context(), "chani")
	lines := c.lines("mcp tools changed")
	if len(lines) != 1 {
		t.Fatalf("%d lines say the tools changed, want 1: %v", len(lines), lines)
	}
	if got := lines[0]; got["principal"] != "chani" || got["added"] != int64(2) || got["removed"] != int64(0) ||
		got["sessions"] != int64(0) {
		t.Errorf("the first line = %v, want chani with 2 added, 0 removed and 0 sessions", got)
	}
	// A wider grant adds the write tool.
	o.grants[key("chani", "notelite")] = []oauth.Item{{Cap: "notelite:read"}, {Cap: "notelite:write"}}
	g.GrantChanged("chani", "notelite")
	lines = c.lines("mcp tools changed")
	if len(lines) != 2 {
		t.Fatalf("%d lines say the tools changed, want 2", len(lines))
	}
	if got := lines[1]; got["added"] != int64(1) || got["removed"] != int64(0) {
		t.Errorf("the second line = %v, want 1 added and 0 removed", got)
	}
	// A narrowed grant removes it again.
	o.grants[key("chani", "notelite")] = []oauth.Item{{Cap: "notelite:read"}}
	g.GrantChanged("chani", "notelite")
	lines = c.lines("mcp tools changed")
	if len(lines) != 3 {
		t.Fatalf("%d lines say the tools changed, want 3", len(lines))
	}
	if got := lines[2]; got["added"] != int64(0) || got["removed"] != int64(1) {
		t.Errorf("the third line = %v, want 0 added and 1 removed", got)
	}
	// A sync that changes nothing says nothing.
	g.GrantChanged("chani", "notelite")
	if n := len(c.lines("mcp tools changed")); n != 3 {
		t.Errorf("%d lines say the tools changed after an unchanged sync, want 3", n)
	}
	// The tools of a read's grant: its 1 read operation, and lodge's own tool.
	got, err := g.tools(t.Context(), "chani")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(got))
	for name := range got {
		names = append(names, name)
	}
	slices.Sort(names)
	if want := []string{"notelite_get_note", "request_access"}; !slices.Equal(names, want) {
		t.Errorf("the tools = %v, want %v", names, want)
	}
}

// TestCallContextOfARevokedClient checks the race between lodge's token check
// and the registration of a request or a call: a client lodge revoked before
// the registration gets a context that is cancelled already, and a revocation
// during a call ends it (ADR-0024, Revoking).
func TestCallContextOfARevokedClient(t *testing.T) {
	g := New(Options{Apps: fakeApps{}, OAuth: &fakeOAuth{}, Log: slog.New(&capture{})})
	g.Revoked("chani", []string{"lgc_a"})
	ctx, done, revoked := g.callContext(context.Background(), Caller{Principal: "chani", Client: "lgc_a",
		Expires: time.Now().Add(time.Hour)})
	defer done()
	if !revoked {
		t.Error("the context of a revoked client is not reported as revoked")
	}
	select {
	case <-ctx.Done():
	default:
		t.Error("the context of a revoked client is not cancelled")
	}
	// A live client's context ends when lodge revokes it in flight.
	live, done2, revoked2 := g.callContext(context.Background(), Caller{Principal: "chani", Client: "lgc_b",
		Expires: time.Now().Add(time.Hour)})
	defer done2()
	if revoked2 {
		t.Error("a live client's context is reported as revoked")
	}
	select {
	case <-live.Done():
		t.Error("a live client's context is cancelled")
	default:
	}
	g.Revoked("chani", []string{"lgc_b"})
	select {
	case <-live.Done():
	default:
		t.Error("the context of a client revoked in flight is not cancelled")
	}
	// And its deadline is its token's.
	short, done3, _ := g.callContext(context.Background(), Caller{Principal: "chani", Client: "lgc_c",
		Expires: time.Now().Add(50 * time.Millisecond)})
	defer done3()
	if deadline, ok := short.Deadline(); !ok || time.Until(deadline) > time.Minute {
		t.Errorf("the context's deadline = %v, %v; want its token's", deadline, ok)
	}
}

// blockingOAuth is a fakeOAuth whose Grant reports each read, and holds the
// first one until the test releases it: 1 sync, with its snapshot in hand.
type blockingOAuth struct {
	mu      sync.Mutex
	grants  map[string][]oauth.Item
	first   bool
	reads   chan struct{}
	release chan struct{}
}

func (f *blockingOAuth) setGrant(principal, app string, items []oauth.Item) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants[key(principal, app)] = items
}

func (f *blockingOAuth) Grant(_ context.Context, principal, app string) ([]oauth.Item, error) {
	f.mu.Lock()
	first := !f.first
	f.first = true
	items := slices.Clone(f.grants[key(principal, app)])
	f.mu.Unlock()
	select {
	case f.reads <- struct{}{}:
	default:
	}
	if first {
		<-f.release
	}
	return items, nil
}

func (f *blockingOAuth) RequestAccess(context.Context, string, string, []oauth.Item) (oauth.Request, error) {
	return oauth.Request{}, nil
}

func (f *blockingOAuth) Live(context.Context, string) (bool, error) { return true, nil }

// TestSyncNewestWins checks that 1 sync at a time keeps the newest grant's
// tools: a sync whose snapshot is older must not apply it after a newer one
// has (ADR-0024, A grant change reaches live sessions).
func TestSyncNewestWins(t *testing.T) {
	o := &blockingOAuth{
		grants:  map[string][]oauth.Item{key("chani", "notelite"): {{Cap: "notelite:read"}}},
		reads:   make(chan struct{}, 8),
		release: make(chan struct{}),
	}
	g := New(Options{Apps: fakeApps{[]apps.App{notesApp()}}, OAuth: o, Log: slog.New(&capture{})})
	// The first sync reads the old grant, and holds it.
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		g.serverFor(context.Background(), "chani")
	}()
	select {
	case <-o.reads:
	case <-time.After(30 * time.Second):
		t.Fatal("the first sync read no grant")
	}
	g.mu.Lock()
	s := g.servers["chani"]
	g.mu.Unlock()
	if s == nil {
		t.Fatal("the first sync made no server")
	}
	// The grant widens, and a second sync starts.
	o.setGrant("chani", "notelite", []oauth.Item{{Cap: "notelite:read"}, {Cap: "notelite:write"}})
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		_ = s.sync(context.Background())
	}()
	// With 1 sync at a time the second waits for the first: the bound is on an
	// event that must not happen, not a sleep that synchronizes them.
	select {
	case <-o.reads:
		t.Log("the second sync read its grant while the first holds the server: no sync lock")
	case <-time.After(300 * time.Millisecond):
	}
	close(o.release)
	select {
	case <-firstDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the first sync did not finish")
	}
	select {
	case <-secondDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the second sync did not finish")
	}
	// The newest grant's tools are the ones that stay.
	s.mu.Lock()
	names := make([]string, 0, len(s.tools))
	for name := range s.tools {
		names = append(names, name)
	}
	s.mu.Unlock()
	slices.Sort(names)
	if want := []string{"notelite_create_note", "notelite_get_note", "request_access"}; !slices.Equal(names, want) {
		t.Errorf("the tools after both syncs = %v, want %v", names, want)
	}
}
