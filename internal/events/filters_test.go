package events_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/events"
	"github.com/frostyard/lodge/internal/oauth"
)

// marksEvents are the events of the app marks: 1 with a folder selector, and
// 1 with none.
var marksEvents = []apps.Event{
	{Type: "org.frostyard.marks.bookmark.created", Message: "marks.v1.BookmarkCreated",
		Topic: "marks.bookmark.created.folder", Selector: "folder", ReadPermission: "marks:read"},
	{Type: "org.frostyard.marks.search.done", Message: "marks.v1.SearchDone",
		Topic: "marks.search.done", ReadPermission: "marks:read"},
}

// item is a grant item on the folder selector, or on the root.
func item(capability, folder string) oauth.Item {
	it := oauth.Item{Cap: capability}
	if folder != "" {
		it.Sel = map[string]string{"folder": folder}
	}
	return it
}

// TestFilters checks the effective read filters of a grant: 1 grant item of
// each shape, and the request that narrows or widens it.
func TestFilters(t *testing.T) {
	for name, c := range map[string]struct {
		events []apps.Event
		items  []oauth.Item
		topic  string
		want   []string
	}{
		"a grant without a selector, on a topic without one": {
			items: []oauth.Item{item("marks:read", "")}, topic: "marks",
			want: []string{"marks.bookmark.created.folder", "marks.bookmark.created.folder.>", "marks.search.done"}},
		"a grant on 1 folder, under the event's topic": {
			items: []oauth.Item{item("marks:read", "/f3")}, topic: "marks.bookmark.created",
			want: []string{"marks.bookmark.created.folder.f3", "marks.bookmark.created.folder.f3.>"}},
		"a grant on 1 folder, on the event's topic": {
			items: []oauth.Item{item("marks:read", "/f3")}, topic: "marks.bookmark.created.folder",
			want: []string{"marks.bookmark.created.folder.f3", "marks.bookmark.created.folder.f3.>"}},
		"a grant on a nested path": {
			items: []oauth.Item{item("marks:read", "/Work/2026")}, topic: "marks",
			want: []string{"marks.bookmark.created.folder.Work.2026", "marks.bookmark.created.folder.Work.2026.>"}},
		"a path that needs encoding": {
			items: []oauth.Item{item("marks:read", "/Work Inbox/Ünïcode")}, topic: "marks.bookmark.created",
			want: []string{"marks.bookmark.created.folder.Work%20Inbox.%C3%9Cn%C3%AFcode",
				"marks.bookmark.created.folder.Work%20Inbox.%C3%9Cn%C3%AFcode.>"}},
		"the root path": {
			items: []oauth.Item{item("marks:read", "/")}, topic: "marks.bookmark.created",
			want: []string{"marks.bookmark.created.folder", "marks.bookmark.created.folder.>"}},
		"an absent path": {
			items: []oauth.Item{item("marks:read", "")}, topic: "marks.bookmark.created",
			want: []string{"marks.bookmark.created.folder", "marks.bookmark.created.folder.>"}},
		"another selector at a path covers no event of the app": {
			items: []oauth.Item{{Cap: "marks:read", Sel: map[string]string{"tag": "/a"}}},
			topic: "marks"},
		"another selector at the root covers every event": {
			items: []oauth.Item{{Cap: "marks:read", Sel: map[string]string{"folder": "/f3", "tag": "/"}}},
			topic: "marks.bookmark.created",
			want:  []string{"marks.bookmark.created.folder.f3", "marks.bookmark.created.folder.f3.>"}},
		"a capability that no event reads": {
			items: []oauth.Item{item("marks:write", "/f3")}, topic: "marks"},
		"no item": {items: nil, topic: "marks"},
		"a request that is narrower than the grant": {
			items: []oauth.Item{item("marks:read", "/")}, topic: "marks.search",
			want: []string{"marks.search.done"}},
		"a request that another event cannot match": {
			items: []oauth.Item{item("marks:read", "")}, topic: "marks.bookmark.removed"},
		"a request with a *": {
			items: []oauth.Item{item("marks:read", "/")}, topic: "marks.*.created",
			want: []string{"marks.bookmark.created.folder", "marks.bookmark.created.folder.>"}},
		"a request on the topic's own token": {
			items: []oauth.Item{item("marks:read", "/")}, topic: "marks.bookmark",
			want: []string{"marks.bookmark.created.folder", "marks.bookmark.created.folder.>"}},
		"nested items collapse into the wider 1": {
			items: []oauth.Item{item("marks:read", "/Work"), item("marks:read", "/Work/2026")},
			topic: "marks.bookmark.created",
			want:  []string{"marks.bookmark.created.folder.Work", "marks.bookmark.created.folder.Work.>"}},
		"a grant that covers another": {
			events: marksEvents,
			items:  []oauth.Item{item("marks:read", ""), item("marks:read", "/f3")},
			topic:  "marks",
			want: []string{"marks.bookmark.created.folder", "marks.bookmark.created.folder.>",
				"marks.search.done"}},
		"a grant on /Chan does not cover /Chani/x": {
			items: []oauth.Item{item("marks:read", "/Chan")}, topic: "marks.bookmark.created",
			want: []string{"marks.bookmark.created.folder.Chan", "marks.bookmark.created.folder.Chan.>"}},
	} {
		t.Run(name, func(t *testing.T) {
			evs := c.events
			if evs == nil {
				evs = marksEvents
			}
			got, err := events.Filters(evs, c.items, c.topic)
			if err != nil {
				t.Fatalf("Filters: %v", err)
			}
			if got == nil {
				t.Fatal("Filters answered nil, want an empty list")
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("Filters(%q) = %q, want %q", c.topic, got, c.want)
			}
		})
	}
}

// TestFiltersRefusesATopic checks the form of a requested topic.
func TestFiltersRefusesATopic(t *testing.T) {
	for name, topic := range map[string]string{
		"empty":           "",
		"no app":          ".marks",
		"a >":             "marks.>",
		"a * first":       "*.bookmark",
		"a space":         "marks.a b",
		"not an app":      "Marks.bookmark",
		"a reserved app":  "lodge.bookmark",
		"an empty token":  "marks..created",
		"a short app id?": "marks_bookmark",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := events.Filters(marksEvents, []oauth.Item{item("marks:read", "/")}, topic)
			if !errors.Is(err, events.ErrInvalid) || got != nil {
				t.Fatalf("Filters(%q) = %q, %v, want an invalid topic", topic, got, err)
			}
		})
	}
}

// TestFiltersTopicLimit checks how many tokens a requested topic may hold: a
// folder 5 levels deep fits.
func TestFiltersTopicLimit(t *testing.T) {
	items := []oauth.Item{item("marks:read", "")}
	tokens := []string{"marks"}
	for range events.MaxTopicTokens - 1 {
		tokens = append(tokens, "a")
	}
	topic := strings.Join(tokens, ".")
	got, err := events.Filters(marksEvents, items, topic)
	if err != nil {
		t.Fatalf("a topic of %d tokens: %v", events.MaxTopicTokens, err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("a topic of %d tokens answered %q, want an empty list", events.MaxTopicTokens, got)
	}
	if _, err := events.Filters(marksEvents, items, topic+".a"); !errors.Is(err, events.ErrInvalid) {
		t.Errorf("a topic of %d tokens answered %v, want an invalid topic", events.MaxTopicTokens+1, err)
	}
	// A folder 5 levels deep is the event's 3 tokens and 6 more.
	deep := "marks.bookmark.created.folder.a.b.c.d.e"
	if _, err := events.Filters(marksEvents, []oauth.Item{item("marks:read", "/a/b/c/d/e")}, deep); err != nil {
		t.Errorf("a topic of a folder 5 levels deep: %v", err)
	}
}

// TestFiltersRefusesAnItemPath checks that a grant item's path is a path.
func TestFiltersRefusesAnItemPath(t *testing.T) {
	_, err := events.Filters(marksEvents, []oauth.Item{{Cap: "marks:read", Sel: map[string]string{"folder": "/a/../b"}}},
		"marks")
	if !errors.Is(err, events.ErrInvalid) {
		t.Fatalf("Filters answered %v, want an invalid path", err)
	}
}

// TestClientName checks the broker's name of an OAuth client: both ways, and
// the names it refuses.
func TestClientName(t *testing.T) {
	const id = "lgc_AAECAwQFBgcICQoLDA0ODw"
	name, ok := events.ClientName(id)
	if !ok {
		t.Fatalf("ClientName(%q) is refused", id)
	}
	if name != "oaaaqeayeaudaocajbifqydiob4" {
		t.Errorf("ClientName(%q) = %q", id, name)
	}
	if back, ok := events.ClientID(name); !ok || back != id {
		t.Errorf("ClientID(%q) = %q, %v, want %q", name, back, ok, id)
	}
	for _, bad := range []string{"", "lgc_", "lgc_short", "xlgc_AAECAwQFBgcICQoLDA0ODw", "lgc_AAECAwQFBgcICQoLDA0ODw=="} {
		if got, ok := events.ClientName(bad); ok {
			t.Errorf("ClientName(%q) = %q, want a refusal", bad, got)
		}
	}
	for _, bad := range []string{"", "o", "x" + name, "oAAAAAAAAAAAAAAAAAAAAAAAAAA", "oaaeaebaqfaycucojcawmnao3d1",
		"oaaeaebaqfaycucojcawmnao3dww"} {
		if got, ok := events.ClientID(bad); ok {
			t.Errorf("ClientID(%q) = %q, want a refusal", bad, got)
		}
	}
}

// TestClientNameRoundTrip checks that every 16 bytes of an ID survive both
// ways, and that the name is 27 characters of the broker's alphabet.
func TestClientNameRoundTrip(t *testing.T) {
	for _, raw := range [][]byte{
		bytes.Repeat([]byte{0x00}, 16),
		bytes.Repeat([]byte{0xff}, 16),
		bytes.Repeat([]byte{0x5a}, 16),
		[]byte("0123456789abcdef"),
	} {
		id := "lgc_" + base64.RawURLEncoding.EncodeToString(raw)
		name, ok := events.ClientName(id)
		if !ok || len(name) != 27 || name[0] != 'o' {
			t.Fatalf("ClientName(%q) = %q, %v", id, name, ok)
		}
		for _, c := range name[1:] {
			if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
				t.Fatalf("the name %q holds %q, which is not the broker's alphabet", name, c)
			}
		}
		if back, ok := events.ClientID(name); !ok || back != id {
			t.Errorf("ClientID(%q) = %q, %v, want %q", name, back, ok, id)
		}
	}
}
