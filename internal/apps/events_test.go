package apps_test

import (
	"strings"
	"testing"

	"github.com/frostyard/lodge/internal/apptest"
)

// TestHearthEvents checks the events a hearth manifest declares: their
// types, their topics and the capability each needs.
func TestHearthEvents(t *testing.T) {
	root := t.TempDir()
	dir := apptest.Install(t, root, "marks", map[string]any{"hearth": "hearth.json"}, nil)
	hearth(t, dir, "hearth.json", `{"hearth":1,"app":"marks",
		"capabilities":["marks:read","marks:write"],"selectors":["folder"],
		"events":[
			{"type":"org.frostyard.marks.bookmark.created","message":"marks.v1.BookmarkCreated",
			 "topic":"marks.bookmark.created.folder","selector":"folder","read_permission":"marks:read"},
			{"type":"org.frostyard.marks.app.cleared","message":"marks.v1.AppCleared",
			 "topic":"marks.app.cleared","read_permission":"marks:write"}]}`)
	a, _ := load(t, root).Get("marks")
	if !a.Enabled() || a.Hearth == nil {
		t.Fatalf("marks is %s: %q", a.State, a.Reason)
	}
	if len(a.Hearth.Events) != 2 {
		t.Fatalf("marks has %d events, want 2", len(a.Hearth.Events))
	}
	got := a.Hearth.Events[0]
	if got.Type != "org.frostyard.marks.bookmark.created" || got.Topic != "marks.bookmark.created.folder" ||
		got.Selector != "folder" || got.ReadPermission != "marks:read" || got.Message != "marks.v1.BookmarkCreated" {
		t.Errorf("the first event is %+v", got)
	}
	if got := a.Hearth.Events[1].Selector; got != "" {
		t.Errorf("an event with no selector reads %q", got)
	}
}

// TestHearthEventDisabled checks each rule of a hearth manifest's events: a
// broken one disables its app, and no other.
func TestHearthEventDisabled(t *testing.T) {
	const base = `{"hearth":1,"app":"a","capabilities":["a:read"],"selectors":["folder"],"events":[`
	for name, c := range map[string]struct {
		body   string
		reason string
	}{
		"a repeated type": {body: `{"type":"org.frostyard.a.x.y","topic":"a.x.y","read_permission":"a:read"},
			{"type":"org.frostyard.a.x.y","topic":"a.x.z","read_permission":"a:read"}`,
			reason: "its type is another event's"},
		"a repeated topic": {body: `{"type":"org.frostyard.a.x.y","topic":"a.x.y","read_permission":"a:read"},
			{"type":"org.frostyard.a.x.z","topic":"a.x.y","read_permission":"a:read"}`,
			reason: "its topic is another event's"},
		"another app's topic": {body: `{"type":"org.frostyard.a.x.y","topic":"b.x.y","read_permission":"a:read"}`,
			reason: `must be a.<object>.<verb>`},
		"a short topic": {body: `{"type":"org.frostyard.a.x","topic":"a.x","read_permission":"a:read"}`,
			reason: `must be a.<object>.<verb>`},
		"a topic token's form": {body: `{"type":"org.frostyard.a.x.y","topic":"a.X.y","read_permission":"a:read"}`,
			reason: "which must match"},
		"an empty topic token": {body: `{"type":"org.frostyard.a.x.y","topic":"a..y","read_permission":"a:read"}`,
			reason: "which must match"},
		"an unknown selector": {body: `{"type":"org.frostyard.a.x.y","topic":"a.x.tag","selector":"tag","read_permission":"a:read"}`,
			reason: `selector "tag" is not one of the app's`},
		"a topic without its selector": {body: `{"type":"org.frostyard.a.x.y","topic":"a.x.y","selector":"folder","read_permission":"a:read"}`,
			reason: `must end with ".folder"`},
		"an unknown read permission": {body: `{"type":"org.frostyard.a.x.y","topic":"a.x.y","read_permission":"a:write"}`,
			reason: `read permission "a:write" is not one of the app's capabilities`},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := apptest.Install(t, root, "a", map[string]any{"hearth": "hearth.json"}, nil)
			hearth(t, dir, "hearth.json", base+c.body+`]}`)
			apptest.Install(t, root, "other", nil, nil)
			r := load(t, root)
			a, ok := r.Get("a")
			if !ok {
				t.Fatal("a is not listed")
			}
			if a.Enabled() || !strings.Contains(a.Reason, c.reason) {
				t.Errorf("state %s, reason %q; want disabled, mentioning %q", a.State, a.Reason, c.reason)
			}
			if o, _ := r.Get("other"); !o.Enabled() {
				t.Errorf("another app is disabled too: %q", o.Reason)
			}
		})
	}
}
