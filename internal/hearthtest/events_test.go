package hearthtest_test

import (
	"slices"
	"strings"
	"testing"

	hearthauthz "github.com/frostyard/hearth/authz"
	hearth "github.com/frostyard/hearth/events"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/events"
	"github.com/frostyard/lodge/internal/oauth"
)

// testEvents are the events of an app, of every shape a grant reads: with a
// selector, with none, and with both.
var testEvents = []apps.Event{
	{Type: "org.frostyard.demo.note.created", Message: "demo.v1.NoteCreated",
		Topic: "demo.note.created.folder", Selector: "folder", ReadPermission: "demo:read"},
	{Type: "org.frostyard.demo.note.deleted", Message: "demo.v1.NoteDeleted",
		Topic: "demo.note.deleted.folder", Selector: "folder", ReadPermission: "demo:write"},
	{Type: "org.frostyard.demo.note.searched", Message: "demo.v1.NoteSearched",
		Topic: "demo.note.searched", ReadPermission: "demo:read"},
}

// testItems are the grants the cross-check reads: a capability on each path
// shape, and items that name another selector.
var testItems = map[string]oauth.Item{
	"the root":                   {Cap: "demo:read"},
	"an absent path":             {Cap: "demo:read", Sel: map[string]string{"folder": ""}},
	"the root path":              {Cap: "demo:read", Sel: map[string]string{"folder": "/"}},
	"1 folder":                   {Cap: "demo:read", Sel: map[string]string{"folder": "/Reading"}},
	"a nested path":              {Cap: "demo:read", Sel: map[string]string{"folder": "/Reading/2026"}},
	"a path to encode":           {Cap: "demo:read", Sel: map[string]string{"folder": "/Work Inbox/x.1"}},
	"the write cap":              {Cap: "demo:write", Sel: map[string]string{"folder": "/Reading"}},
	"no event's cap":             {Cap: "demo:admin"},
	"another selector at a path": {Cap: "demo:read", Sel: map[string]string{"tag": "/a"}},
	"another selector at the root": {Cap: "demo:read",
		Sel: map[string]string{"folder": "/Reading", "tag": "/"}},
}

// specs are testEvents as hearth's own event specs.
func specs() []hearth.Spec {
	out := make([]hearth.Spec, 0, len(testEvents))
	for _, ev := range testEvents {
		out = append(out, hearth.Spec{App: "demo", Type: ev.Type, Topic: ev.Topic, Selector: ev.Selector,
			ReadPermission: ev.ReadPermission})
	}
	return out
}

// hearthFilters is hearth's own answer for a set of items, in lodge's shape:
// the filters of every item, each turned into the pattern that it stands for,
// with lodge's covering removal.
func hearthFilters(t *testing.T, items []oauth.Item) []string {
	t.Helper()
	var filters []string
	for _, it := range items {
		got, err := hearth.ReadFilters(specs(), hearthauthz.Item{Cap: it.Cap, Sel: it.Sel})
		if err != nil {
			t.Fatalf("hearth's ReadFilters(%+v): %v", it, err)
		}
		filters = append(filters, got...)
	}
	return renderPatterns(filters)
}

// pattern is 1 filter group that hearth's filters stand for: a subject, and
// whether it takes every subject below it.
type pattern struct {
	subject string
	deep    bool
}

// renderPatterns turns hearth's filters into the filters lodge answers: a
// topic and its pattern, <topic>.>, are 1 deep pattern, a lonely topic is an
// exact one, and a pattern that another covers goes.
func renderPatterns(filters []string) []string {
	have := map[string]bool{}
	for _, f := range filters {
		have[f] = true
	}
	var pats []pattern
	for _, f := range filters {
		if strings.HasSuffix(f, ".>") {
			pats = append(pats, pattern{subject: strings.TrimSuffix(f, ".>"), deep: true})
			continue
		}
		if have[f+".>"] {
			// The topic and its pattern are 1 deep pattern.
			continue
		}
		pats = append(pats, pattern{subject: f})
	}
	var kept []pattern
	for _, a := range pats {
		covered := false
		for _, b := range pats {
			if a == b || !coversPattern(b, a) {
				continue
			}
			covered = true
			break
		}
		if !covered {
			kept = append(kept, a)
		}
	}
	out := []string{}
	for _, p := range kept {
		out = append(out, p.subject)
		if p.deep {
			out = append(out, p.subject+".>")
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// coversPattern reports whether pattern b takes every subject that pattern a
// takes.
func coversPattern(b, a pattern) bool {
	bt, at := strings.Split(b.subject, "."), strings.Split(a.subject, ".")
	if len(bt) > len(at) {
		return false
	}
	for i, tok := range bt {
		if tok != at[i] {
			return false
		}
	}
	return b.deep || (len(bt) == len(at) && !a.deep)
}

// TestFiltersMatchHearth checks that lodge's read filters are hearth's own:
// for each item, the union of hearth's ReadFilters, after the covering
// removal, is what lodge's Filters answers for the app's whole subject.
func TestFiltersMatchHearth(t *testing.T) {
	for name, one := range testItems {
		t.Run(name, func(t *testing.T) {
			items := []oauth.Item{one}
			got, err := events.Filters(testEvents, items, "demo")
			if err != nil {
				t.Fatalf("lodge's Filters: %v", err)
			}
			want := hearthFilters(t, items)
			if !slices.Equal(got, want) {
				t.Errorf("lodge's filters are %q; hearth's are %q", got, want)
			}
		})
	}
	// A grant that reads several events at once: the union of them all.
	for name, items := range map[string][]oauth.Item{
		"every item":     {testItems["the root"], testItems["1 folder"], testItems["the write cap"]},
		"nested items":   {testItems["the root"], testItems["a nested path"]},
		"other selector": {testItems["another selector at a path"], testItems["the root"]},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := events.Filters(testEvents, items, "demo")
			if err != nil {
				t.Fatalf("lodge's Filters: %v", err)
			}
			want := hearthFilters(t, items)
			if !slices.Equal(got, want) {
				t.Errorf("lodge's filters are %q; hearth's are %q", got, want)
			}
		})
	}
}
