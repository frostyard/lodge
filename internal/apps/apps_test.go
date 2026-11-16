package apps_test

import (
	"cmp"
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/apptest"
)

func load(t *testing.T, root string) *apps.Registry {
	t.Helper()
	r := apps.New(root, nil)
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLoadValidApp(t *testing.T) {
	root := t.TempDir()
	apptest.Install(t, root, "notes", map[string]any{"name": "Notes", "key": "n", "content": true, "mcp": true,
		"futureField": map[string]any{"x": 1}}, nil)
	r := load(t, root)
	a, ok := r.Get("notes")
	if !ok {
		t.Fatal("notes is not loaded")
	}
	if !a.Enabled() || a.Key != "n" || !a.Manifest.Content || !a.Manifest.MCP {
		t.Errorf("notes = %+v", a)
	}
	if a.State != apps.Down {
		t.Errorf("before a poll notes is %s, want down", a.State)
	}
}

func TestDisabled(t *testing.T) {
	cases := map[string]struct {
		id       string
		manifest map[string]any
		files    func(dir string)
		reason   string
	}{
		"contract 2":       {id: "a", manifest: map[string]any{"contract": 2}, reason: "contract 2"},
		"no contract":      {id: "a", manifest: map[string]any{"contract": nil}, reason: "contract 0"},
		"id mismatch":      {id: "a", manifest: map[string]any{"id": "b"}, reason: "directory"},
		"bad id":           {id: "a", manifest: map[string]any{"id": "A"}, reason: "must match"},
		"long name":        {id: "a", manifest: map[string]any{"name": strings.Repeat("n", 33)}, reason: "name"},
		"empty name":       {id: "a", manifest: map[string]any{"name": ""}, reason: "name"},
		"png icon":         {id: "a", manifest: map[string]any{"icon": "icon.png"}, reason: "icon"},
		"escaping icon":    {id: "a", manifest: map[string]any{"icon": "../icon.svg"}, reason: "icon"},
		"bad version":      {id: "a", manifest: map[string]any{"version": "1 0"}, reason: "version"},
		"root user":        {id: "a", manifest: map[string]any{"user": "root"}, reason: "user"},
		"lodge user":       {id: "a", manifest: map[string]any{"user": "lodge"}, reason: "user"},
		"bad key":          {id: "a", manifest: map[string]any{"key": "N"}, reason: "key"},
		"wrong type":       {id: "a", manifest: map[string]any{"content": "yes"}, reason: "app.json"},
		"no index":         {id: "a", files: func(d string) { os.Remove(filepath.Join(d, "web/index.html")) }, reason: "index.html"},
		"no icon":          {id: "a", files: func(d string) { os.Remove(filepath.Join(d, "web/icon.svg")) }, reason: "icon"},
		"hidden icon":      {id: "a", manifest: map[string]any{"icon": ".i/icon.svg"}, files: hiddenIcon, reason: "cannot be served"},
		"symlinked dir":    {id: "a", manifest: map[string]any{"icon": "img/icon.svg"}, files: symlinkDir, reason: "cannot be served"},
		"symlinked index":  {id: "a", files: symlinkIndex, reason: "index.html"},
		"unparsable":       {id: "a", files: func(d string) { os.WriteFile(filepath.Join(d, "app.json"), []byte("{"), 0o644) }, reason: "app.json"},
		"duplicate member": {id: "a", files: dupMember, reason: "app.json"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := apptest.Install(t, root, c.id, c.manifest, nil)
			if c.files != nil {
				c.files(dir)
			}
			apptest.Install(t, root, "other", nil, nil)
			r := load(t, root)
			a, ok := r.Get(c.id)
			if !ok {
				t.Fatal("the app is not listed")
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

func symlinkIndex(dir string) {
	idx := filepath.Join(dir, "web/index.html")
	os.Rename(idx, filepath.Join(dir, "real.html"))
	os.Symlink(filepath.Join(dir, "real.html"), idx)
}

func hiddenIcon(dir string) {
	os.MkdirAll(filepath.Join(dir, "web/.i"), 0o755)
	os.WriteFile(filepath.Join(dir, "web/.i/icon.svg"), []byte("<svg/>"), 0o644)
}

func symlinkDir(dir string) {
	os.MkdirAll(filepath.Join(dir, "real"), 0o755)
	os.WriteFile(filepath.Join(dir, "real/icon.svg"), []byte("<svg/>"), 0o644)
	os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "web/img"))
}

func dupMember(dir string) {
	os.WriteFile(filepath.Join(dir, "app.json"), []byte(`{"contract":1,"id":"a","id":"a","name":"a","icon":"icon.svg","version":"1","user":"x"}`), 0o644)
}

func TestReservedDirectoriesSkipped(t *testing.T) {
	root := t.TempDir()
	apptest.Install(t, root, "lodge", nil, nil)
	apptest.Install(t, root, "Bad", nil, nil)
	if got := load(t, root).List(); len(got) != 0 {
		t.Errorf("List = %+v, want none", got)
	}
}

func TestSharedUserDisablesBoth(t *testing.T) {
	root := t.TempDir()
	apptest.Install(t, root, "a", map[string]any{"user": "shared"}, nil)
	apptest.Install(t, root, "b", map[string]any{"user": "shared"}, nil)
	apptest.Install(t, root, "c", nil, nil)
	r := load(t, root)
	for _, id := range []string{"a", "b"} {
		if a, _ := r.Get(id); a.Enabled() {
			t.Errorf("%s is enabled with a shared user", id)
		}
	}
	if c, _ := r.Get("c"); !c.Enabled() {
		t.Error("c is disabled")
	}
}

func TestSharedKeyClearsBoth(t *testing.T) {
	root := t.TempDir()
	apptest.Install(t, root, "a", map[string]any{"key": "x"}, nil)
	apptest.Install(t, root, "b", map[string]any{"key": "x"}, nil)
	apptest.Install(t, root, "c", map[string]any{"key": "y"}, nil)
	r := load(t, root)
	for id, want := range map[string]string{"a": "", "b": "", "c": "y"} {
		a, _ := r.Get(id)
		if !a.Enabled() || a.Key != want {
			t.Errorf("%s: enabled %v, key %q; want enabled, key %q", id, a.Enabled(), a.Key, want)
		}
		if lost := want == ""; lost != (len(a.Notes) == 1 && a.Notes[0] == "key x is claimed by more than one app") {
			t.Errorf("%s: notes %q", id, a.Notes)
		}
	}
}

func TestListOrderedByName(t *testing.T) {
	root := t.TempDir()
	apptest.Install(t, root, "zed", map[string]any{"name": "Alpha"}, nil)
	apptest.Install(t, root, "abc", map[string]any{"name": "beta"}, nil)
	got := load(t, root).List()
	if len(got) != 2 || got[0].ID != "zed" || got[1].ID != "abc" {
		t.Errorf("List order = %v, %v", got[0].ID, got[1].ID)
	}
}

func TestPoll(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"up", "degraded", "broken", "silent", "off"} {
		apptest.Install(t, root, id, nil, nil)
	}
	os.Remove(filepath.Join(root, "usr/share/lodge/apps/off/web/index.html"))
	var mu sync.Mutex
	var callers []string
	apptest.Serve(t, root, "up", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callers = append(callers, r.Header.Get("Lodge-Caller")+"|"+r.URL.Path)
		mu.Unlock()
		apptest.Healthy("2.0")(w, r)
	}))
	apptest.Serve(t, root, "degraded", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"ok":false,"reason":"opening the vault"}`))
	}))
	apptest.Serve(t, root, "broken", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	r := load(t, root)
	r.Poll(t.Context())
	want := map[string]apps.State{"up": apps.Up, "degraded": apps.Degraded, "broken": apps.Down, "silent": apps.Down, "off": apps.Disabled}
	for id, st := range want {
		a, _ := r.Get(id)
		if a.State != st {
			t.Errorf("%s is %s (%s), want %s", id, a.State, a.Reason, st)
		}
	}
	if a, _ := r.Get("up"); a.Version != "2.0" || a.Protocol != 1 {
		t.Errorf("up's health = %q, %d", a.Version, a.Protocol)
	}
	if a, _ := r.Get("degraded"); a.Reason != "opening the vault" {
		t.Errorf("degraded's reason = %q", a.Reason)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(callers) != 1 || callers[0] != "|/health" {
		t.Errorf("health requests = %q, want one /health without Lodge-Caller", callers)
	}
	// A reload keeps the last health of an app that stays.
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	if a, _ := r.Get("up"); a.State != apps.Up {
		t.Errorf("after a reload up is %s", a.State)
	}
}

func TestPollBadge(t *testing.T) {
	root := t.TempDir()
	ids := []string{"counted", "text", "fraction", "gone"}
	for _, id := range ids {
		apptest.Install(t, root, id, nil, nil)
	}
	var mu sync.Mutex
	bodies := map[string]string{
		"counted":  `{"ok":true,"version":"1.0","protocol":1,"badge":3}`,
		"text":     `{"ok":true,"version":"1.0","protocol":1,"badge":"3"}`,
		"fraction": `{"ok":true,"version":"1.0","protocol":1,"badge":2.5}`,
		"gone":     `{"ok":true,"version":"1.0","protocol":1,"badge":3}`,
	}
	for _, id := range ids {
		apptest.Serve(t, root, id, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			body := bodies[id]
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
	}
	r := load(t, root)
	r.Poll(t.Context())
	for id, want := range map[string]struct {
		state apps.State
		badge int
	}{
		"counted":  {apps.Up, 3},
		"text":     {apps.Up, 0},
		"fraction": {apps.Up, 0},
		"gone":     {apps.Up, 3},
	} {
		a, _ := r.Get(id)
		if a.State != want.state || a.Badge != want.badge {
			t.Errorf("%s is %s with badge %d, want %s with %d", id, a.State, a.Badge, want.state, want.badge)
		}
	}
	// A reload keeps an app's count, as it keeps its state.
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	if a, _ := r.Get("counted"); a.State != apps.Up || a.Badge != 3 {
		t.Errorf("after a reload counted is %s with badge %d", a.State, a.Badge)
	}
	// An app that is no longer up shows no count.
	mu.Lock()
	bodies["gone"] = `{"ok":false,"reason":"closing the vault"}`
	mu.Unlock()
	r.Poll(t.Context())
	if a, _ := r.Get("gone"); a.State != apps.Degraded || a.Badge != 0 {
		t.Errorf("gone is %s with badge %d, want degraded with 0", a.State, a.Badge)
	}
}

func TestReloadAddsAndRemoves(t *testing.T) {
	root := t.TempDir()
	apptest.Install(t, root, "a", nil, nil)
	r := load(t, root)
	apptest.Install(t, root, "b", nil, nil)
	os.RemoveAll(filepath.Join(root, "usr/share/lodge/apps/a"))
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("a"); ok {
		t.Error("a is still listed")
	}
	if _, ok := r.Get("b"); !ok {
		t.Error("b is not listed")
	}
}

func TestNoAppsDirectory(t *testing.T) {
	if got := load(t, t.TempDir()).List(); len(got) != 0 {
		t.Errorf("List = %v", got)
	}
}

// One enabled app whose manifest sets companion is the companion. When
// two do, neither is, both stay enabled, and each says why (ADR-0013).
func TestAtMostOneCompanion(t *testing.T) {
	root := t.TempDir()
	apptest.Install(t, root, "porter", map[string]any{"companion": true}, nil)
	apptest.Install(t, root, "notes", nil, nil)
	apptest.Install(t, root, "off", map[string]any{"companion": true, "contract": 9}, nil)
	r := load(t, root)
	if a, _ := r.Get("porter"); !a.Companion || len(a.Notes) != 0 {
		t.Errorf("porter: companion %v, notes %q", a.Companion, a.Notes)
	}
	if a, _ := r.Get("notes"); a.Companion {
		t.Error("notes is the companion")
	}
	if a, _ := r.Get("off"); a.Companion {
		t.Error("a disabled app is the companion")
	}
	apptest.Install(t, root, "aide", map[string]any{"companion": true}, nil)
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"aide", "porter"} {
		a, _ := r.Get(id)
		if !a.Enabled() || a.Companion || len(a.Notes) != 1 || !strings.Contains(a.Notes[0], "companion is claimed by more than one app: aide, porter") {
			t.Errorf("%s: enabled %v, companion %v, notes %q", id, a.Enabled(), a.Companion, a.Notes)
		}
	}
}

func TestPlaceholderIcon(t *testing.T) {
	root := t.TempDir()
	scaffold := `<svg xmlns="http://www.w3.org/2000/svg" ` + apps.PlaceholderIcon + `="replace me" viewBox="0 0 24 24"/>`
	apptest.Install(t, root, "tasks", nil, map[string]string{"icon.svg": scaffold})
	apptest.Install(t, root, "notes", nil, nil)
	r := load(t, root)
	if a, _ := r.Get("tasks"); !a.Enabled() || len(a.Notes) != 1 || !strings.Contains(a.Notes[0], "web/icon.svg is the scaffold's placeholder") {
		t.Errorf("tasks: enabled %v, notes %q", a.Enabled(), a.Notes)
	}
	if a, _ := r.Get("notes"); !a.Enabled() || len(a.Notes) != 0 {
		t.Errorf("notes: enabled %v, notes %q", a.Enabled(), a.Notes)
	}
}

// hearth writes a hearth manifest to dir.
func hearth(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHearthManifest(t *testing.T) {
	root := t.TempDir()
	dir := apptest.Install(t, root, "marks", map[string]any{"hearth": "hearth.json"}, nil)
	hearth(t, dir, "hearth.json", `{"hearth":1,"app":"marks","version":"1.0.0","capabilities":["marks:read","marks:write"],"selectors":["folder"],"other":[1]}`)
	r := load(t, root)
	a, ok := r.Get("marks")
	if !ok || !a.Enabled() {
		t.Fatalf("marks = %+v, enabled %v", a, ok)
	}
	if a.Hearth == nil {
		t.Fatal("marks has no hearth manifest")
	}
	if got := strings.Join(a.Hearth.Capabilities, " "); got != "marks:read marks:write" {
		t.Errorf("capabilities %q", got)
	}
	if got := strings.Join(a.Hearth.Selectors, " "); got != "folder" {
		t.Errorf("selectors %q", got)
	}
	if a.Hearth.Version != "1.0.0" || a.Hearth.Major != "1" {
		t.Errorf("version %q, major %q; want 1.0.0 and 1", a.Hearth.Version, a.Hearth.Major)
	}
	// A manifest with no members is an empty one, not an error.
	dir2 := apptest.Install(t, root, "empty", map[string]any{"hearth": "h.json"}, nil)
	hearth(t, dir2, "h.json", `{"hearth":1,"app":"empty","version":"1.0.0"}`)
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	if a, _ := r.Get("empty"); !a.Enabled() || a.Hearth == nil || len(a.Hearth.Capabilities) != 0 {
		t.Errorf("empty = %+v", a)
	}
}

func TestHearthManifestDisabled(t *testing.T) {
	for name, c := range map[string]struct {
		files  func(t *testing.T, dir string)
		reason string
	}{
		"format 2": {files: func(t *testing.T, dir string) { hearth(t, dir, "hearth.json", `{"hearth":2,"app":"a"}`) },
			reason: "hearth manifest format 2 is not one this lodge reads (1)"},
		"no format": {files: func(t *testing.T, dir string) { hearth(t, dir, "hearth.json", `{"app":"a"}`) },
			reason: "hearth manifest format 0 is not one this lodge reads (1)"},
		"other app": {files: func(t *testing.T, dir string) {
			hearth(t, dir, "hearth.json", `{"hearth":1,"app":"b","version":"1.0.0"}`)
		},
			reason: `names app "b", not "a"`},
		"not json": {files: func(t *testing.T, dir string) { hearth(t, dir, "hearth.json", `{`) }, reason: "hearth manifest"},
		"missing":  {files: func(t *testing.T, dir string) {}, reason: "hearth manifest"},
		"capability without app": {files: func(t *testing.T, dir string) {
			hearth(t, dir, "hearth.json", `{"hearth":1,"app":"a","version":"1.0.0","capabilities":["read"]}`)
		}, reason: "capability"},
		"capability of another app": {files: func(t *testing.T, dir string) {
			hearth(t, dir, "hearth.json", `{"hearth":1,"app":"a","version":"1.0.0","capabilities":["b:read"]}`)
		}, reason: "capability"},
		"bad capability name": {files: func(t *testing.T, dir string) {
			hearth(t, dir, "hearth.json", `{"hearth":1,"app":"a","version":"1.0.0","capabilities":["a:Read"]}`)
		}, reason: "capability"},
		"bad selector": {files: func(t *testing.T, dir string) {
			hearth(t, dir, "hearth.json", `{"hearth":1,"app":"a","version":"1.0.0","selectors":["Folder"]}`)
		}, reason: "selector"},
		"no version": {files: func(t *testing.T, dir string) {
			hearth(t, dir, "hearth.json", `{"hearth":1,"app":"a"}`)
		}, reason: `has version "", not MAJOR.MINOR.PATCH`},
		"bad version": {files: func(t *testing.T, dir string) {
			hearth(t, dir, "hearth.json", `{"hearth":1,"app":"a","version":"1.0"}`)
		}, reason: `has version "1.0", not MAJOR.MINOR.PATCH`},
		"pre-release version": {files: func(t *testing.T, dir string) {
			hearth(t, dir, "hearth.json", `{"hearth":1,"app":"a","version":"1.0.0-rc.1"}`)
		}, reason: `has version "1.0.0-rc.1", not MAJOR.MINOR.PATCH`},
		"hidden file": {files: func(t *testing.T, dir string) {
			hearth(t, dir, ".hearth.json", `{"hearth":1,"app":"a","version":"1.0.0"}`)
		}, reason: "app.json"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			file := "hearth.json"
			if name == "hidden file" {
				file = ".hearth.json"
			}
			dir := apptest.Install(t, root, "a", map[string]any{"hearth": file}, nil)
			c.files(t, dir)
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

// TestHearthMajor checks the compatibility line of a version, which a grant
// item is approved for: the major from 1.0.0, and 0.MINOR before it (hearth
// ADR-0004, Compatibility).
func TestHearthMajor(t *testing.T) {
	for version, want := range map[string]string{
		"0.1.0": "0.1", "0.1.9": "0.1", "0.2.0": "0.2", "0.10.3": "0.10",
		"1.0.0": "1", "1.4.2": "1", "2.0.0": "2", "12.0.1": "12",
		"": "", "1": "", "1.0": "", "01.0.0": "", "1.00.0": "", "1.0.0-rc.1": "", "v1.0.0": "", "1.0.0+b": "",
	} {
		got, ok := apps.HearthMajor(version)
		if got != want || ok != (want != "") {
			t.Errorf("HearthMajor(%q) = %q, %v; want %q", version, got, ok, want)
		}
	}
}

func TestHearthTooLarge(t *testing.T) {
	root := t.TempDir()
	dir := apptest.Install(t, root, "a", map[string]any{"hearth": "hearth.json"}, nil)
	body := `{"hearth":1,"app":"a","version":"1.0.0","selectors":["` + strings.Repeat("s", apps.MaxHearth) + `"]}`
	hearth(t, dir, "hearth.json", body)
	a, _ := load(t, root).Get("a")
	if a.Enabled() || !strings.Contains(a.Reason, "larger than") {
		t.Errorf("a huge hearth manifest: %s %q", a.State, a.Reason)
	}
	// A symbolic link is not the app's file.
	dir2 := apptest.Install(t, root, "b", map[string]any{"hearth": "hearth.json"}, nil)
	hearth(t, dir2, "real.json", `{"hearth":1,"app":"b","version":"1.0.0"}`)
	os.Symlink(filepath.Join(dir2, "real.json"), filepath.Join(dir2, "hearth.json"))
	if a, _ := load(t, root).Get("b"); a.Enabled() || !strings.Contains(a.Reason, "regular file") {
		t.Errorf("a symlinked hearth manifest: %s %q", a.State, a.Reason)
	}
}

// noteOps are the 4 operations of hearth's notes-lite example, in the shape
// of its manifest.
func noteOps(id string) []map[string]any {
	op := func(name, permission, effect string, expose []string, tool string) map[string]any {
		return map[string]any{"name": name, "permission": permission, "effect": effect,
			"expose": expose, "selectors": []any{}, "tool": tool}
	}
	return []map[string]any{
		op("example.notes.v1.NoteService.CreateNote", id+":write", "write", []string{"ui", "agent"}, "create_note"),
		op("example.notes.v1.NoteService.DeleteNote", id+":write", "write", []string{"ui", "agent"}, "delete_note"),
		op("example.notes.v1.NoteService.GetNote", id+":read", "read", []string{"ui", "agent"}, "get_note"),
		op("example.notes.v1.NoteService.ListNotes", id+":read", "read", []string{"ui", "agent"}, "list_notes"),
	}
}

// noteSchema is a tool's schema, with a member beyond type, which a catalog
// may hold.
func noteSchema(title string) map[string]any {
	return map[string]any{"type": "object", "title": title,
		"properties": map[string]any{"folder": map[string]any{"type": "string"}}}
}

// noteTools are the catalog's tools, in the shape of an MCP tools/list
// result, for noteOps.
func noteTools() []map[string]any {
	tool := func(name, description string) map[string]any {
		return map[string]any{"name": name, "description": description,
			"inputSchema": noteSchema(name + " in"), "outputSchema": noteSchema(name + " out"),
			"annotations": map[string]any{"readOnlyHint": false}}
	}
	return []map[string]any{
		tool("create_note", "Make a note."),
		tool("delete_note", "Delete a note."),
		tool("get_note", "Read a note."),
		tool("list_notes", "List the notes."),
	}
}

// writeJSON writes v as JSON to dir/name.
func writeJSON(t *testing.T, dir, name string, v any) {
	t.Helper()
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	hearth(t, dir, name, string(b))
}

// TestHearthOperationsAndTools checks that an app on hearth loads its
// operations and its tool catalog: the notes-lite example's 4 operations,
// each with its tool, and the schemas as compact JSON.
func TestHearthOperationsAndTools(t *testing.T) {
	root := t.TempDir()
	dir := apptest.Install(t, root, "notelite", map[string]any{"hearth": "hearth.json", "tools": "tools.json"}, nil)
	writeJSON(t, dir, "hearth.json", map[string]any{"hearth": 1, "app": "notelite", "version": "1.0.0",
		"capabilities": []string{"notelite:read", "notelite:write"}, "selectors": []string{"folder"},
		"operations": noteOps("notelite"), "events": []any{}})
	writeJSON(t, dir, "tools.json", map[string]any{"tools": noteTools(), "other": 1})
	a, ok := load(t, root).Get("notelite")
	if !ok || !a.Enabled() {
		t.Fatalf("notelite = %+v, enabled %v", a, ok)
	}
	if a.Hearth == nil || len(a.Hearth.Operations) != 4 {
		t.Fatalf("notelite's operations = %+v", a.Hearth)
	}
	first := a.Hearth.Operations[0]
	if first.Name != "example.notes.v1.NoteService.CreateNote" || first.Permission != "notelite:write" ||
		first.Effect != "write" || !first.Agent() || !first.UI() || first.Tool != "create_note" {
		t.Errorf("the first operation = %+v", first)
	}
	if got := first.Service(); got != "example.notes.v1.NoteService" {
		t.Errorf("Service = %q", got)
	}
	if got := first.Method(); got != "CreateNote" {
		t.Errorf("Method = %q", got)
	}
	if got := a.Hearth.Operations[3].Service(); got != "example.notes.v1.NoteService" {
		t.Errorf("the last operation's Service = %q", got)
	}
	if got := a.Hearth.Operations[3].Method(); got != "ListNotes" {
		t.Errorf("the last operation's Method = %q", got)
	}
	// A tool is 1 agent operation's, with its description and its schemas in
	// their canonical compact form.
	if len(a.Hearth.Tools) != 4 {
		t.Fatalf("notelite's tools = %+v", a.Hearth.Tools)
	}
	tool := a.Hearth.Tools[0]
	want := compactJSON(t, noteSchema("create_note in"))
	if tool.Name != "create_note" || tool.Description != "Make a note." {
		t.Errorf("the first tool = %+v", tool)
	}
	if string(tool.InputSchema) != want {
		t.Errorf("inputSchema = %s, want %s", tool.InputSchema, want)
	}
	if string(tool.OutputSchema) != compactJSON(t, noteSchema("create_note out")) {
		t.Errorf("outputSchema = %s", tool.OutputSchema)
	}
	// The catalog's other members are ignored, as its tools' are.
	if got := a.Hearth.Tools[3]; got.Name != "list_notes" || got.Description != "List the notes." {
		t.Errorf("the last tool = %+v", got)
	}
}

// compactJSON is v as canonical JSON, with no space outside a string.
func compactJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestHearthOperationDisabled checks each rule of a hearth manifest's
// operations: a break disables the app, and the reason names the manifest and
// the operation.
func TestHearthOperationDisabled(t *testing.T) {
	one := func(edit func(ops []map[string]any)) []map[string]any {
		ops := noteOps("notelite")
		edit(ops)
		return ops
	}
	cases := map[string]struct {
		ops    []map[string]any
		reason string
	}{
		"2 parts":  {ops: one(func(ops []map[string]any) { ops[0]["name"] = "example.NoteService" }), reason: "at least 3 parts"},
		"bad name": {ops: one(func(ops []map[string]any) { ops[0]["name"] = "example.notes.v1.Create-Note" }), reason: "its name must be"},
		"repeated name": {ops: one(func(ops []map[string]any) { ops[1]["name"] = ops[0]["name"] }),
			reason: "is another operation's"},
		"unknown permission": {ops: one(func(ops []map[string]any) { ops[0]["permission"] = "notelite:admin" }),
			reason: "is not one of the app's capabilities"},
		"bad effect": {ops: one(func(ops []map[string]any) { ops[0]["effect"] = "delete" }),
			reason: "is not read, write or outward"},
		"empty expose": {ops: one(func(ops []map[string]any) { ops[0]["expose"] = []string{} }),
			reason: "its expose is empty"},
		"repeated expose": {ops: one(func(ops []map[string]any) { ops[0]["expose"] = []string{"ui", "ui"} }),
			reason: "with a repeat"},
		"unknown expose": {ops: one(func(ops []map[string]any) { ops[0]["expose"] = []string{"api"} }),
			reason: "not ui or agent"},
		"agent without tool": {ops: one(func(ops []map[string]any) { delete(ops[0], "tool") }),
			reason: "an agent operation needs its tool's name"},
		"tool without agent": {ops: one(func(ops []map[string]any) { ops[0]["expose"] = []string{"ui"} }),
			reason: "not exposed to agents"},
		"bad tool": {ops: one(func(ops []map[string]any) { ops[0]["tool"] = "Create_Note" }),
			reason: "must match ^[a-z][a-z0-9_]*$"},
		"repeated tool": {ops: one(func(ops []map[string]any) { ops[1]["tool"] = ops[0]["tool"] }),
			reason: "is another operation's"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := apptest.Install(t, root, "notelite", map[string]any{"hearth": "hearth.json", "tools": "tools.json"}, nil)
			writeJSON(t, dir, "hearth.json", map[string]any{"hearth": 1, "app": "notelite", "version": "1.0.0",
				"capabilities": []string{"notelite:read", "notelite:write"}, "selectors": []string{"folder"},
				"operations": c.ops})
			writeJSON(t, dir, "tools.json", map[string]any{"tools": noteTools()})
			apptest.Install(t, root, "other", nil, nil)
			r := load(t, root)
			a, _ := r.Get("notelite")
			if a.Enabled() || !strings.Contains(a.Reason, c.reason) || !strings.Contains(a.Reason, "hearth.json") {
				t.Errorf("state %s, reason %q; want disabled, naming %q and the manifest", a.State, a.Reason, c.reason)
			}
			if o, _ := r.Get("other"); !o.Enabled() {
				t.Errorf("another app is disabled too: %q", o.Reason)
			}
		})
	}
}

// TestToolCatalogDisabled checks each rule of an app's tool catalog: a break
// disables the app, and the reason names the catalog file.
func TestToolCatalogDisabled(t *testing.T) {
	one := func(edit func(tools []map[string]any)) []map[string]any {
		tools := noteTools()
		edit(tools)
		return tools
	}
	type testCase struct {
		id      string
		noTools bool
		noFile  bool
		ops     []map[string]any
		tools   []map[string]any
		body    any
		raw     string
		files   func(t *testing.T, dir string)
		reason  string
		// names is the file name the reason holds, or "" when it names none.
		names string
	}
	cases := map[string]testCase{
		"agent operations and no tools": {noTools: true,
			reason: "its hearth manifest has agent operations, and app.json names no tools"},
		"no catalog file": {noFile: true, reason: "its tool catalog tools.json", names: "tools.json"},
		"not JSON":        {raw: `{`, reason: "its tool catalog tools.json", names: "tools.json"},
		"not an object":   {raw: `[]`, reason: "its tool catalog tools.json", names: "tools.json"},
		"tools not a list": {body: map[string]any{"tools": 5}, reason: "its tool catalog tools.json",
			names: "tools.json"},
		"too many tools": {tools: func() []map[string]any {
			tools := make([]map[string]any, 129)
			for i := range tools {
				tools[i] = map[string]any{"name": "t" + strconv.Itoa(i), "description": "",
					"inputSchema": map[string]any{"type": "object"}}
			}
			return tools
		}(), reason: "more than 128", names: "tools.json"},
		"bad tool name": {tools: one(func(tools []map[string]any) { tools[0]["name"] = "Create_note" }),
			reason: "its name must match ^[a-z][a-z0-9_]*$", names: "tools.json"},
		"long tool name": {tools: one(func(tools []map[string]any) {
			tools[0]["name"] = strings.Repeat("n", 48)
		}), reason: "longer than 47 bytes", names: "tools.json"},
		"repeated tool name": {tools: one(func(tools []map[string]any) { tools[1]["name"] = "create_note" }),
			reason: "its name is another tool's", names: "tools.json"},
		"not an operation's tool": {tools: one(func(tools []map[string]any) { tools[0]["name"] = "write_note" }),
			reason: "not the tool of an agent operation", names: "tools.json"},
		"description is not a string": {tools: one(func(tools []map[string]any) { tools[0]["description"] = 5 }),
			reason: "its description is not a string", names: "tools.json"},
		"long description": {tools: one(func(tools []map[string]any) {
			tools[0]["description"] = strings.Repeat("d", 4097)
		}), reason: "longer than 4096 bytes", names: "tools.json"},
		"inputSchema is not an object": {tools: one(func(tools []map[string]any) { tools[0]["inputSchema"] = "object" }),
			reason: "its inputSchema is not a JSON object whose type is object", names: "tools.json"},
		"inputSchema is not an object type": {tools: one(func(tools []map[string]any) {
			tools[0]["inputSchema"] = map[string]any{"type": "string"}
		}), reason: "its inputSchema is not a JSON object whose type is object", names: "tools.json"},
		"inputSchema without a type": {tools: one(func(tools []map[string]any) {
			tools[0]["inputSchema"] = map[string]any{"properties": map[string]any{}}
		}), reason: "its inputSchema is not a JSON object whose type is object", names: "tools.json"},
		"outputSchema is not an object": {tools: one(func(tools []map[string]any) { tools[1]["outputSchema"] = "x" }),
			reason: "its outputSchema is not a JSON object whose type is object", names: "tools.json"},
		"outputSchema is not an object type": {tools: one(func(tools []map[string]any) {
			tools[1]["outputSchema"] = map[string]any{"type": "array"}
		}), reason: "its outputSchema is not a JSON object whose type is object", names: "tools.json"},
		"an operation without a tool": {tools: noteTools()[:3],
			reason: `has no tool "list_notes"`, names: "tools.json"},
		"the gateway's own name": {id: "request",
			ops:    []map[string]any{{"name": "example.notes.v1.NoteService.Access", "permission": "request:read", "effect": "read", "expose": []string{"agent"}, "tool": "access"}},
			tools:  []map[string]any{{"name": "access", "description": "", "inputSchema": map[string]any{"type": "object"}}},
			reason: "the gateway's tool request_access is lodge's own", names: "tools.json"},
		"a symbolic link": {files: func(t *testing.T, dir string) {
			hearth(t, dir, "real.json", `{"tools":[]}`)
			if err := os.Remove(filepath.Join(dir, "tools.json")); err != nil {
				t.Fatal(err)
			}
			os.Symlink(filepath.Join(dir, "real.json"), filepath.Join(dir, "tools.json"))
		}, reason: "is not a regular file", names: "tools.json"},
		"too large": {raw: `{"tools":[],"pad":"` + strings.Repeat("p", apps.MaxHearth) + `"}`,
			reason: "larger than 1 MiB", names: "tools.json"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			id := cmp.Or(c.id, "notelite")
			root := t.TempDir()
			app := map[string]any{"hearth": "hearth.json", "tools": "tools.json"}
			if c.noTools {
				app["tools"] = nil
			}
			dir := apptest.Install(t, root, id, app, nil)
			ops := c.ops
			if ops == nil {
				ops = noteOps(id)
			}
			writeJSON(t, dir, "hearth.json", map[string]any{"hearth": 1, "app": id, "version": "1.0.0",
				"capabilities": []string{id + ":read", id + ":write"}, "operations": ops})
			switch {
			case c.raw != "":
				hearth(t, dir, "tools.json", c.raw)
			case c.body != nil:
				writeJSON(t, dir, "tools.json", c.body)
			case c.tools != nil:
				writeJSON(t, dir, "tools.json", map[string]any{"tools": c.tools})
			case !c.noFile:
				writeJSON(t, dir, "tools.json", map[string]any{"tools": noteTools()})
			}
			if c.files != nil {
				c.files(t, dir)
			}
			apptest.Install(t, root, "other", nil, nil)
			r := load(t, root)
			a, _ := r.Get(id)
			if a.Enabled() || !strings.Contains(a.Reason, c.reason) {
				t.Fatalf("state %s, reason %q; want disabled, naming %q", a.State, a.Reason, c.reason)
			}
			if c.names != "" && !strings.Contains(a.Reason, c.names) {
				t.Errorf("the reason %q does not name %q", a.Reason, c.names)
			}
			if o, _ := r.Get("other"); !o.Enabled() {
				t.Errorf("another app is disabled too: %q", o.Reason)
			}
		})
	}
}

// TestToolCatalogOptionalMembers checks what a catalog and a manifest may
// leave out: an empty description, no output schema or a null one, and a
// manifest with no agent operation and no catalog.
func TestToolCatalogOptionalMembers(t *testing.T) {
	root := t.TempDir()
	dir := apptest.Install(t, root, "notelite", map[string]any{"hearth": "hearth.json", "tools": "tools.json"}, nil)
	writeJSON(t, dir, "hearth.json", map[string]any{"hearth": 1, "app": "notelite", "version": "1.0.0",
		"capabilities": []string{"notelite:read", "notelite:write"}, "operations": noteOps("notelite")})
	tools := noteTools()
	tools[0]["description"] = ""
	delete(tools[1], "outputSchema")
	tools[2]["outputSchema"] = nil
	writeJSON(t, dir, "tools.json", map[string]any{"tools": tools})
	// An app with no agent operation needs no catalog.
	dir2 := apptest.Install(t, root, "plain", map[string]any{"hearth": "h.json"}, nil)
	writeJSON(t, dir2, "h.json", map[string]any{"hearth": 1, "app": "plain", "version": "1.0.0", "capabilities": []string{"plain:read"},
		"operations": []map[string]any{{"name": "example.plain.v1.PlainService.Get", "permission": "plain:read",
			"effect": "read", "expose": []string{"ui"}}}})
	r := load(t, root)
	a, _ := r.Get("notelite")
	if !a.Enabled() {
		t.Fatalf("notelite = %s %q", a.State, a.Reason)
	}
	if got := a.Hearth.Tools[0].Description; got != "" {
		t.Errorf("an empty description = %q", got)
	}
	for _, i := range []int{1, 2} {
		if got := a.Hearth.Tools[i].OutputSchema; len(got) != 0 {
			t.Errorf("tool %d's absent outputSchema = %s", i, got)
		}
	}
	if p, _ := r.Get("plain"); !p.Enabled() || p.Hearth == nil || len(p.Hearth.Tools) != 0 {
		t.Errorf("plain = %+v", p)
	}
}
