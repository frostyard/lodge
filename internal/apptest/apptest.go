// Package apptest installs fake apps under a temporary layout root for
// lodge's tests: a manifest, a web bundle, and an HTTP server on the app's
// socket (docs/specs/app-contract.md, Install layout).
package apptest

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Install writes app id's manifest and a bundle under root. manifest's
// fields override the defaults (contract 1, id, name, icon "icon.svg",
// version, user "app<id>"); a nil value removes a field. files are extra
// files under web/, by relative path.
func Install(t testing.TB, root, id string, manifest map[string]any, files map[string]string) string {
	t.Helper()
	m := map[string]any{"contract": 1, "id": id, "name": id, "icon": "icon.svg", "version": "1.0.0", "user": "app" + id}
	for k, v := range manifest {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	dir := filepath.Join(root, "usr/share/lodge/apps", id)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "app.json"), string(b))
	all := map[string]string{
		"index.html": "<!doctype html><title>" + id + "</title><p>" + id + " index</p>",
		"icon.svg":   `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`,
	}
	for k, v := range files {
		all[k] = v
	}
	for rel, body := range all {
		write(t, filepath.Join(dir, "web", rel), body)
	}
	return dir
}

// InstallShell writes a shell bundle under root.
func InstallShell(t testing.TB, root string, files map[string]string) {
	t.Helper()
	all := map[string]string{
		"index.html":           "<!doctype html><title>Lodge</title><p>shell</p>",
		"manifest.webmanifest": `{"name":"Lodge"}`,
	}
	for k, v := range files {
		all[k] = v
	}
	for rel, body := range all {
		write(t, filepath.Join(root, "usr/share/lodge/shell", rel), body)
	}
}

func write(t testing.TB, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Serve serves h on app id's socket under root until the test ends.
func Serve(t testing.TB, root, id string, h http.Handler) {
	t.Helper()
	sock := filepath.Join(root, "run/lodge/apps", id+".sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve %s: %v", id, err)
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		srv.Close()
		<-done
	})
}

// Healthy answers /health as an app that is up.
func Healthy(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"version":"` + version + `","protocol":1}`))
	}
}
