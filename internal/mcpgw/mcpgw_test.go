package mcpgw

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"

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
