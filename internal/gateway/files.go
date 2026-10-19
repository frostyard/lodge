package gateway

import (
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
)

// filePathRE is a servable path: every segment a plain name, none hidden
// or empty (docs/specs/lodge-http.md, App files).
var filePathRE = regexp.MustCompile(`^(/[A-Za-z0-9_-][A-Za-z0-9._-]*)+$`)

var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".ico":   "image/x-icon",
	".webp":  "image/webp",
	".woff2": "font/woff2",
	".woff":  "font/woff",
	".ttf":   "font/ttf",
	".wasm":  "application/wasm",
}

type fileOpts struct {
	// shell serves .webmanifest as a web manifest (the shell's only).
	shell bool
	// fallback serves index.html for a path that is no file, except under
	// assets/; noFallback answers 404.
	fallback, noFallback bool
}

// serveFile serves rel (starting with "/") from dir, by the path rules of
// App files. The caller has set its kind's headers.
func (g *Gateway) serveFile(w http.ResponseWriter, r *http.Request, dir, rel string, o fileOpts) {
	f, fi, ok := openPlain(dir, rel)
	if !ok {
		if !o.fallback || strings.HasPrefix(rel, "/assets/") || rel == "/index.html" {
			fail(w, http.StatusNotFound, "not found", nil)
			return
		}
		rel = "/index.html"
		if f, fi, ok = openPlain(dir, rel); !ok {
			fail(w, http.StatusNotFound, "not found", nil)
			return
		}
	}
	defer f.Close()
	h := w.Header()
	ext := path.Ext(rel)
	ct, known := contentTypes[ext]
	switch {
	case known:
	case o.shell && ext == ".webmanifest":
		ct = "application/manifest+json"
	default:
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	if strings.HasPrefix(rel, "/assets/") {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// openPlain opens rel under dir when it matches filePathRE, no element of
// it is a symbolic link, and it names a regular file.
func openPlain(dir, rel string) (*os.File, os.FileInfo, bool) {
	if !filePathRE.MatchString(rel) {
		return nil, nil, false
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, false
	}
	defer root.Close()
	p := ""
	for seg := range strings.SplitSeq(rel[1:], "/") {
		p = path.Join(p, seg)
		fi, err := root.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			return nil, nil, false
		}
	}
	f, err := root.Open(p)
	if err != nil {
		return nil, nil, false
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, nil, false
	}
	return f, fi, true
}
