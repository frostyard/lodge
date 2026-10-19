package gateway

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	shellPolicy = "default-src 'none'; script-src https://lodge.example/lodge/; style-src https://lodge.example/lodge/ 'unsafe-inline'; img-src 'self' data:; font-src https://lodge.example/lodge/ data:; connect-src https://lodge.example/lodge/; manifest-src https://lodge.example/lodge/; frame-src https://lodge.example/app/; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	notesPolicy = "default-src 'none'; script-src https://lodge.example/app/notes/; style-src https://lodge.example/app/notes/ 'unsafe-inline'; img-src 'self' data: https://lodge-content.example https:; media-src 'self' https://lodge-content.example; font-src https://lodge.example/app/notes/ data:; connect-src https://lodge.example/app/notes/ wss://lodge.example/app/notes/api; frame-src https://lodge.example/app/notes/ https://lodge-content.example; manifest-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"
	plainPolicy = "default-src 'none'; script-src https://lodge.example/app/plain/; style-src https://lodge.example/app/plain/ 'unsafe-inline'; img-src 'self' data:; media-src 'self'; font-src https://lodge.example/app/plain/ data:; connect-src https://lodge.example/app/plain/ wss://lodge.example/app/plain/api; frame-src https://lodge.example/app/plain/; manifest-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"
	mailPolicy  = "default-src 'none'; script-src https://lodge.example/app/mail/; style-src https://lodge.example/app/mail/ 'unsafe-inline'; img-src 'self' data: https://lodge-content.example; media-src 'self' https://lodge-content.example; font-src https://lodge.example/app/mail/ data:; connect-src https://lodge.example/app/mail/ wss://lodge.example/app/mail/api; frame-src https://lodge.example/app/mail/ https://lodge-content.example; manifest-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"
)

func checkBrowserHeaders(t *testing.T, where string, h http.Header, csp, frame string) {
	t.Helper()
	want := map[string]string{
		"X-Frame-Options":              frame,
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=()",
	}
	for k, v := range want {
		if got := h.Values(k); len(got) != 1 || got[0] != v {
			t.Errorf("%s: %s = %q, want %q", where, k, got, v)
		}
	}
	if got := h.Values("Content-Security-Policy"); len(got) != 1 || got[0] != csp {
		t.Errorf("%s: CSP = %q\nwant %q", where, got, csp)
	}
	for k := range h {
		if k == "Set-Cookie" || strings.HasPrefix(k, "Access-Control-") {
			t.Errorf("%s: carries %s", where, k)
		}
	}
}

func TestHeaders(t *testing.T) {
	h := newHarness(t)
	shell := []string{"/", "/notes", "/notes/a/b", "/lodge/apps", "/lodge/shell.js", "/lodge/missing.js", "/nosuchapp"}
	for _, p := range shell {
		checkBrowserHeaders(t, p, h.get(p, nil).header, shellPolicy, "DENY")
		checkBrowserHeaders(t, p+" (refused)", h.get(p, map[string]string{"Origin": "https://evil.example"}).header, shellPolicy, "DENY")
	}
	for p, csp := range map[string]string{
		"/app/notes/": notesPolicy, "/app/notes/style.css": notesPolicy, "/app/notes/assets/missing.js": notesPolicy,
		"/app/notes/x/read": notesPolicy, "/app/plain/": plainPolicy, "/app/mail/": mailPolicy, "/app/nosuchapp/": strings.ReplaceAll(plainPolicy, "plain", "nosuchapp"),
	} {
		checkBrowserHeaders(t, p, h.get(p, nil).header, csp, "SAMEORIGIN")
		checkBrowserHeaders(t, p+" (refused)", h.get(p, map[string]string{"Sec-Fetch-Site": "cross-site"}).header, csp, "SAMEORIGIN")
	}
}

// An app's x/ response cannot widen lodge's headers: its own policy is
// kept beside lodge's, and the rest are replaced or dropped.
func TestAppResponseCannotWidenHeaders(t *testing.T) {
	h := newHarness(t)
	h.notes.handle = func(w http.ResponseWriter, r *http.Request) bool {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "img-src 'none'")
		hd.Set("X-Frame-Options", "ALLOWALL")
		hd.Set("Cross-Origin-Resource-Policy", "cross-origin")
		hd.Set("Referrer-Policy", "unsafe-url")
		hd.Set("Set-Cookie", "a=b")
		hd.Set("Access-Control-Allow-Origin", "*")
		hd.Set("Access-Control-Allow-Credentials", "true")
		hd.Set("X-App", "kept")
		w.Write([]byte("ok"))
		return true
	}
	a := h.get("/app/notes/x/read", nil)
	if got := a.header.Values("Content-Security-Policy"); len(got) != 2 || got[0] != "img-src 'none'" || got[1] != notesPolicy {
		t.Errorf("CSP = %q, want the app's then lodge's", got)
	}
	a.header.Del("Content-Security-Policy")
	a.header.Set("Content-Security-Policy", notesPolicy)
	checkBrowserHeaders(t, "x/", a.header, notesPolicy, "SAMEORIGIN")
	if a.header.Get("X-App") != "kept" {
		t.Error("the app's own header was dropped")
	}
}

func TestAppFiles(t *testing.T) {
	h := newHarness(t)
	web := filepath.Join(h.root, "usr/share/lodge/apps/notes/web")
	os.WriteFile(filepath.Join(web, ".secret"), []byte("hidden"), 0o644)
	os.MkdirAll(filepath.Join(web, ".git"), 0o755)
	os.WriteFile(filepath.Join(web, ".git/config"), []byte("hidden"), 0o644)
	os.WriteFile(filepath.Join(h.root, "outside.txt"), []byte("outside"), 0o644)
	os.Symlink(filepath.Join(h.root, "outside.txt"), filepath.Join(web, "link.txt"))
	os.Symlink(filepath.Join(web, "sub"), filepath.Join(web, "linkdir"))
	os.Symlink(filepath.Join(h.root, "outside.txt"), filepath.Join(web, "assets/link.js"))
	os.MkdirAll(filepath.Join(web, "adir"), 0o755)

	files := map[string]struct{ body, ctype, cache string }{
		"/app/notes/assets/main-abc.js": {"js", "text/javascript; charset=utf-8", "private, max-age=31536000, immutable"},
		"/app/notes/style.css":          {"css", "text/css; charset=utf-8", "no-cache"},
		"/app/notes/data.json":          {"{}", "application/json", "no-cache"},
		"/app/notes/icon.svg":           {"<svg", "image/svg+xml", "no-cache"},
		"/app/notes/font.woff2":         {"w", "font/woff2", "no-cache"},
		"/app/notes/blob.bin":           {"b", "application/octet-stream", "no-cache"},
		"/app/notes/sub/page.html":      {"p", "text/html; charset=utf-8", "no-cache"},
		"/app/notes/index.html":         {"notes index", "text/html; charset=utf-8", "no-cache"},
	}
	for p, w := range files {
		a := h.get(p, nil)
		if a.status != http.StatusOK || !strings.Contains(a.body, w.body) || a.header.Get("Content-Type") != w.ctype || a.header.Get("Cache-Control") != w.cache {
			t.Errorf("%s: %d %q %q %q", p, a.status, a.body, a.header.Get("Content-Type"), a.header.Get("Cache-Control"))
		}
	}
	// Anything else serves index.html: client routes, hidden names,
	// symbolic links, directories, and path tricks.
	for _, p := range []string{"/app/notes", "/app/notes/", "/app/notes/1/Projects/Plan.md", "/app/notes/.secret", "/app/notes/.git/config",
		"/app/notes/link.txt", "/app/notes/linkdir/page.html", "/app/notes/adir", "/app/notes/sub", "/app/notes/a//b", "/app/notes/x.html/"} {
		a := h.get(p, nil)
		if a.status != http.StatusOK || !strings.Contains(a.body, "notes index") || a.header.Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d %q, want index.html", p, a.status, a.body)
		}
	}
	// Under assets/, a missing file is 404.
	for _, p := range []string{"/app/notes/assets/missing-123.js", "/app/notes/assets/link.js", "/app/notes/assets/.hidden"} {
		expect(t, h.get(p, nil), http.StatusNotFound, "not found")
	}
	// Ranges and HEAD work on files.
	a := h.get("/app/notes/style.css", map[string]string{"Range": "bytes=1-2"})
	if a.status != http.StatusPartialContent || a.body != "ss" {
		t.Errorf("a range: %d %q", a.status, a.body)
	}
}

func TestShellRoutes(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/", "/notes", "/notes/1/Projects/Plan.md", "/mail/thread/42", "/off"} {
		a := h.get(p+"?q=1", nil)
		if a.status != http.StatusOK || !strings.Contains(a.body, "shell") || a.header.Get("Cache-Control") != "no-cache" ||
			a.header.Get("Content-Type") != "text/html; charset=utf-8" {
			t.Errorf("%s: %d %q", p, a.status, a.body)
		}
	}
	a := h.get("/lodge/manifest.webmanifest", nil)
	if a.status != http.StatusOK || a.header.Get("Content-Type") != "application/manifest+json" {
		t.Errorf("the web manifest: %d %q", a.status, a.header.Get("Content-Type"))
	}
	if a := h.get("/app/notes/x.webmanifest", nil); a.header.Get("Content-Type") == "application/manifest+json" {
		t.Error("an app serves a web manifest")
	}
	if a := h.get("/lodge/assets/app-1.js", nil); a.header.Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Errorf("shell asset caching: %q", a.header.Get("Cache-Control"))
	}
	// No index.html fallback under /lodge/.
	for _, p := range []string{"/lodge", "/lodge/", "/lodge/missing", "/lodge/notes/1"} {
		expect(t, h.get(p, nil), http.StatusNotFound, "")
	}
}

func TestAppsList(t *testing.T) {
	h := newHarness(t)
	a := h.get("/lodge/apps", nil)
	if a.status != http.StatusOK || a.header.Get("Cache-Control") != "no-store" || a.header.Get("Content-Type") != "application/json" {
		t.Fatalf("/lodge/apps: %d %v", a.status, a.header)
	}
	var got rail
	if err := json.Unmarshal([]byte(a.body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Contract != 1 || got.ContentOrigin != "https://"+contentHost || len(got.Apps) != 4 {
		t.Fatalf("rail = %+v", got)
	}
	names := []string{}
	for _, x := range got.Apps {
		names = append(names, x.Name)
	}
	if strings.Join(names, ",") != "Mail,Notes,Off,Plain" {
		t.Errorf("order = %v, want by name", names)
	}
	want := map[string]railApp{
		"notes": {ID: "notes", Name: "Notes", Icon: "/app/notes/icon.svg", Key: "n", Content: true, State: "up"},
		"plain": {ID: "plain", Name: "Plain", Icon: "/app/plain/icon.svg", State: "down", Reason: "its socket does not answer"},
	}
	for _, x := range got.Apps {
		if w, ok := want[x.ID]; ok && x != w {
			t.Errorf("%s = %+v, want %+v", x.ID, x, w)
		}
		if x.ID == "off" && (x.State != "disabled" || !strings.Contains(x.Reason, "contract 9") || x.Icon != "") {
			t.Errorf("off = %+v", x)
		}
	}
}
