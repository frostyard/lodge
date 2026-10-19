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
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/frostyard/lodge/kit/manifest"
)

// Contracts are the contract majors this lodge speaks.
var Contracts = []int{manifest.Contract}

// Manifest is an app's app.json, the kit's type.
type Manifest = manifest.Manifest

// ValidID reports whether id may name an app.
func ValidID(id string) bool { return manifest.ValidID(id) }

// servable is a path lodge's file rules serve: every segment a plain name.
var servable = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)*$`)

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

// loadOne loads app id with the kit's manifest.Load, then checks what
// lodge adds: a contract major it speaks, and an index and icon it will
// serve (no symbolic link on the way, no hidden segment).
func (r *Registry) loadOne(id string) App {
	a := App{ID: id, State: Disabled}
	dir := r.AppDir(id)
	m, err := manifest.Load(dir)
	if err != nil {
		a.Manifest = loose(dir)
		a.Reason = oneLine(err.Error())
		return a
	}
	a.Manifest = m
	web := filepath.Join(dir, "web")
	switch {
	case !slices.Contains(Contracts, m.Contract):
		a.Reason = fmt.Sprintf("contract %d is not one this lodge speaks (%v)", m.Contract, Contracts)
	case !regularFile(web, "index.html"):
		a.Reason = "web/index.html is not a regular file"
	case !servable.MatchString(m.Icon) || !regularFile(web, m.Icon):
		a.Reason = fmt.Sprintf("its icon web/%s cannot be served", m.Icon)
	default:
		a.Key = m.Key
		a.State, a.Reason = Down, "not polled yet"
	}
	return a
}

// loose reads what it can of a manifest that does not load, so the rail
// can still name the app.
func loose(dir string) Manifest {
	var m Manifest
	if b, err := os.ReadFile(filepath.Join(dir, "app.json")); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	if utf8Len := len([]rune(m.Name)); utf8Len < 1 || utf8Len > 32 {
		m.Name = ""
	}
	return Manifest{Name: m.Name}
}

// oneLine joins a multi-line error into one line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

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
