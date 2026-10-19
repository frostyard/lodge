package apps_test

import (
	"net/http"
	"os"
	"path/filepath"
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
