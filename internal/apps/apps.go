// Package apps reads the installed apps' manifests and polls their health
// (docs/specs/app-contract.md, Install layout and The manifest). Each app is
// up, degraded, down or disabled, with a reason, for the rail
// (/lodge/apps) and lodgectl. A disabled app's routes answer 503; the other
// apps are unaffected.
package apps

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// State is an app's state on the rail.
type State string

// The states (docs/specs/lodge-http.md, Shell routes).
const (
	Up       State = "up"
	Degraded State = "degraded"
	Down     State = "down"
	Disabled State = "disabled"
)

// Health polling (docs/specs/app-contract.md, What an app serves).
const (
	PollEvery   = 15 * time.Second
	PollTimeout = 2 * time.Second
)

// App is one installed app as lodge sees it.
type App struct {
	ID       string   `json:"id"`
	Manifest Manifest `json:"manifest"`
	// Key is the switcher key in effect: the manifest's, unless another
	// manifest claims it too.
	Key    string `json:"key"`
	State  State  `json:"state"`
	Reason string `json:"reason"`
	// Version and Protocol are the last healthy /health answer's.
	Version  string `json:"version,omitempty"`
	Protocol int    `json:"protocol,omitempty"`
}

// Enabled reports whether the app is served.
func (a App) Enabled() bool { return a.State != Disabled }

// Registry is the installed apps.
type Registry struct {
	root string
	log  *slog.Logger
	http *http.Client

	mu   sync.RWMutex
	apps map[string]App
}

// New returns a registry for the layout under root ("" for /). It reads
// nothing until Load.
func New(root string, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &Registry{root: root, log: log, apps: map[string]App{}}
	r.http = &http.Client{Transport: &http.Transport{DialContext: r.DialContext, DisableKeepAlives: true}, Timeout: PollTimeout}
	return r
}

// AppsDir is where manifests and bundles are installed.
func (r *Registry) AppsDir() string { return filepath.Join(r.root, "/usr/share/lodge/apps") }

// AppDir is app id's install directory.
func (r *Registry) AppDir(id string) string { return filepath.Join(r.AppsDir(), id) }

// SocketPath is app id's socket.
func (r *Registry) SocketPath(id string) string {
	return filepath.Join(r.root, "/run/lodge/apps", id+".sock")
}

// DialContext dials the socket of the app named by addr's host
// ("<id>:80"), for an http.Transport.
func (r *Registry) DialContext(ctx context.Context, _, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if !ValidID(host) {
		return nil, fmt.Errorf("dial app %q: not an app ID", host)
	}
	var d net.Dialer
	return d.DialContext(ctx, "unix", r.SocketPath(host))
}

// Load reads every manifest, replacing the registry's apps. An app that
// stays keeps its last health until the next poll.
func (r *Registry) Load() error {
	entries, err := os.ReadDir(r.AppsDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read the apps: %w", err)
	}
	loaded := map[string]App{}
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || !ValidID(id) {
			r.log.Warn("skip an entry in the apps directory", "name", id)
			continue
		}
		loaded[id] = r.loadOne(id)
	}
	disableShared(loaded)

	r.mu.Lock()
	defer r.mu.Unlock()
	for id, a := range loaded {
		if old, ok := r.apps[id]; ok && a.Enabled() && old.Enabled() {
			a.State, a.Reason, a.Version, a.Protocol = old.State, old.Reason, old.Version, old.Protocol
			loaded[id] = a
		}
		if !a.Enabled() {
			r.log.Warn("app disabled", "app", id, "reason", a.Reason)
		}
	}
	r.apps = loaded
	return nil
}

func (r *Registry) loadOne(id string) App {
	a := App{ID: id, State: Disabled}
	dir := r.AppDir(id)
	b, err := os.ReadFile(filepath.Join(dir, "app.json"))
	if err != nil {
		a.Reason = "app.json cannot be read"
		return a
	}
	m, err := ParseManifest(b, id)
	a.Manifest = m
	if err != nil {
		a.Reason = err.Error()
		return a
	}
	web := filepath.Join(dir, "web")
	if !regularFile(web, "index.html") {
		a.Reason = "web/index.html is missing"
		return a
	}
	if !regularFile(web, m.Icon) {
		a.Reason = fmt.Sprintf("its icon web/%s is missing", m.Icon)
		return a
	}
	a.Key = m.Key
	a.State, a.Reason = Down, "not polled yet"
	return a
}

// regularFile reports whether rel names a regular file under dir with no
// symbolic link on the way.
func regularFile(dir, rel string) bool {
	p := dir
	for seg := range strings.SplitSeq(rel, "/") {
		p = filepath.Join(p, seg)
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// disableShared disables both apps of a shared user, and takes a shared
// key from every app that claims it.
func disableShared(apps map[string]App) {
	users := map[string][]string{}
	keys := map[string][]string{}
	for id, a := range apps {
		if a.Manifest.User != "" && a.Enabled() {
			users[a.Manifest.User] = append(users[a.Manifest.User], id)
		}
		if a.Key != "" {
			keys[a.Key] = append(keys[a.Key], id)
		}
	}
	for user, ids := range users {
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			a := apps[id]
			a.State, a.Reason, a.Key = Disabled, fmt.Sprintf("user %q is claimed by more than one app", user), ""
			apps[id] = a
		}
	}
	for _, ids := range keys {
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			a := apps[id]
			a.Key = ""
			apps[id] = a
		}
	}
}

// Get returns app id.
func (r *Registry) Get(id string) (App, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.apps[id]
	return a, ok
}

// List returns every app, ordered by name.
func (r *Registry) List() []App {
	r.mu.RLock()
	out := make([]App, 0, len(r.apps))
	for _, a := range r.apps {
		out = append(out, a)
	}
	r.mu.RUnlock()
	slices.SortFunc(out, func(a, b App) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Manifest.Name), strings.ToLower(b.Manifest.Name)), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// Poll asks every enabled app's /health once, concurrently.
func (r *Registry) Poll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, a := range r.List() {
		if !a.Enabled() {
			continue
		}
		wg.Go(func() {
			h := r.health(ctx, a.ID)
			r.mu.Lock()
			defer r.mu.Unlock()
			cur, ok := r.apps[a.ID]
			if !ok || !cur.Enabled() {
				return
			}
			if cur.State != h.State {
				r.log.Info("app state", "app", a.ID, "state", h.State, "reason", h.Reason)
			}
			cur.State, cur.Reason = h.State, h.Reason
			if h.State == Up {
				cur.Version, cur.Protocol = h.Version, h.Protocol
			}
			r.apps[a.ID] = cur
		})
	}
	wg.Wait()
}

// Run polls every PollEvery until ctx ends.
func (r *Registry) Run(ctx context.Context) {
	t := time.NewTicker(PollEvery)
	defer t.Stop()
	for {
		r.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type healthAnswer struct {
	OK       bool   `json:"ok"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
	Reason   string `json:"reason"`
}

// health asks app id's /health, as lodge's poll: no Lodge-Caller.
func (r *Registry) health(ctx context.Context, id string) App {
	ctx, cancel := context.WithTimeout(ctx, PollTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+id+"/health", nil)
	if err != nil {
		return App{State: Down, Reason: err.Error()}
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return App{State: Down, Reason: "its socket does not answer"}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var h healthAnswer
	if err != nil || json.Unmarshal(body, &h) != nil {
		return App{State: Down, Reason: fmt.Sprintf("/health answered %d without its JSON", resp.StatusCode)}
	}
	switch {
	case h.OK && resp.StatusCode == http.StatusOK:
		return App{State: Up, Version: h.Version, Protocol: h.Protocol}
	case !h.OK:
		return App{State: Degraded, Reason: cmp.Or(truncate(h.Reason, 200), "the app says it is not ready")}
	default:
		return App{State: Down, Reason: fmt.Sprintf("/health answered %d", resp.StatusCode)}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
