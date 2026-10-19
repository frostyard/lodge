// Package gateway is lodge's router (docs/specs/lodge-http.md;
// docs/design/overview.md, The gateway). It sorts each request by node and
// path into a kind (browser, agent, device or content), runs that kind's
// checks and only those, finds the app, and then serves a file itself or
// proxies to the app's socket. One function per kind of response sets the
// headers, and an app's response cannot widen them.
package gateway

import (
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/frostyard/lodge/internal/allow"
	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/peer"
	"github.com/frostyard/lodge/internal/sessions"
	"github.com/frostyard/lodge/internal/tokens"
)

// Contract is the contract major the shell speaks, sent in /lodge/apps.
const Contract = 1

// Connection limits (docs/specs/lodge-http.md, Connections).
const (
	// MaxHeaderBytes bounds a request's header.
	MaxHeaderBytes = 64 << 10
	// MaxConnsPerApp bounds lodge's open HTTP connections to one app's
	// socket; sessions and device sockets are bounded apart.
	MaxConnsPerApp = 128
)

// Config is what the gateway needs.
type Config struct {
	Names    Names
	Root     string // the layout root, "" for /
	Apps     *apps.Registry
	Allow    *allow.Lists
	Tokens   *tokens.Store
	Sessions *sessions.Registry
	Log      *slog.Logger     // [discard]
	Now      func() time.Time // [time.Now]
	// BodyTimeout bounds each read of a device request's body, so a peer
	// that stalls a body cannot hold a connection to an app [30 s].
	BodyTimeout time.Duration
}

// Gateway serves lodge's routes.
type Gateway struct {
	cfg       Config
	log       *slog.Logger
	transport *http.Transport
	ws        *http.Client
	streams   *streams
}

// New returns a gateway over cfg.
func New(cfg Config) *Gateway {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.BodyTimeout == 0 {
		cfg.BodyTimeout = 30 * time.Second
	}
	tr := &http.Transport{
		DialContext:         cfg.Apps.DialContext,
		MaxIdleConnsPerHost: 8,
		// Requests past this many open connections to one app wait for one.
		MaxConnsPerHost:    MaxConnsPerApp,
		IdleConnTimeout:    90 * time.Second,
		DisableCompression: true,
	}
	return &Gateway{
		cfg:       cfg,
		log:       cfg.Log,
		transport: tr,
		// WebSocket dials take their own connection each.
		ws:      &http.Client{Transport: &http.Transport{DialContext: cfg.Apps.DialContext, DisableKeepAlives: true, DisableCompression: true}},
		streams: newStreams(),
	}
}

func (g *Gateway) now() time.Time { return g.cfg.Now() }

// Server returns an HTTP server for h that serves HTTP/1.1 only and gives
// each connection its own WhoIs cache.
func Server(h http.Handler) *http.Server {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    MaxHeaderBytes,
		ConnContext:       connContext,
		// An empty, non-nil map keeps HTTP/2 off on TLS connections.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	srv.Protocols = new(http.Protocols)
	srv.Protocols.SetHTTP1(true)
	return srv
}

// Run sweeps idle sessions every second and re-checks them every minute,
// until ctx ends.
func (g *Gateway) Run(ctx context.Context) {
	sweep := time.NewTicker(time.Second)
	defer sweep.Stop()
	recheck := time.NewTicker(sessions.RecheckEvery)
	defer recheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweep.C:
			g.cfg.Sessions.Sweep(g.now())
		case <-recheck.C:
			g.cfg.Sessions.Recheck(ctx)
		}
	}
}

// fail answers status with one line, after setHeaders sets its kind's
// headers.
func fail(w http.ResponseWriter, status int, line string, setHeaders func(http.Header)) {
	h := w.Header()
	if setHeaders != nil {
		setHeaders(h)
	}
	// A refused request does not keep its connection.
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusMethodNotAllowed,
		http.StatusUpgradeRequired, http.StatusTooManyRequests, http.StatusBadRequest:
		h.Set("Connection", "close")
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Del("Content-Length")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(oneLine(line) + "\n"))
}

func failWith(w http.ResponseWriter, d *denial, setHeaders func(http.Header), allow string) {
	if d.status == http.StatusMethodNotAllowed && allow != "" {
		w.Header().Set("Allow", allow)
	}
	fail(w, d.status, d.line, setHeaders)
}

// oneLine replaces control characters, so a line stays one line.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

// segments splits an escaped path into its unescaped segments. ok is
// false for a path with a dot segment or a bad escape, which no route
// takes.
func segments(escaped string) (segs []string, ok bool) {
	if !strings.HasPrefix(escaped, "/") {
		return nil, false
	}
	for raw := range strings.SplitSeq(escaped[1:], "/") {
		seg, err := url.PathUnescape(raw)
		if err != nil || seg == "." || seg == ".." || raw == "." || raw == ".." {
			return nil, false
		}
		segs = append(segs, seg)
	}
	return segs, true
}

// rest is the escaped path after its first n segments, starting with "/",
// or "" when there is nothing after them.
func rest(escaped string, n int) string {
	p := escaped
	for range n {
		i := strings.IndexByte(p[1:], '/')
		if i < 0 {
			return ""
		}
		p = p[i+1:]
	}
	return p
}

// Lodge serves the lodge node's routes, naming peers with who.
func (g *Gateway) Lodge(who peer.WhoIs) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escaped := r.URL.EscapedPath()
		segs, ok := segments(escaped)
		if !ok || r.URL.Opaque != "" {
			fail(w, http.StatusNotFound, "not found", g.shellHeaders)
			return
		}
		switch first := segs[0]; {
		case escaped == "/":
			g.shellPage(w, r, who, "")
		case first == "lodge":
			g.shellFiles(w, r, who, segs)
		case first == "app" && len(segs) >= 2 && apps.ValidID(segs[1]):
			g.appRoute(w, r, who, segs[1], escaped)
		case first == "mcp" && len(segs) == 2 && apps.ValidID(segs[1]):
			g.mcp(w, r, who, segs[1])
		case first == "device" && len(segs) >= 2 && apps.ValidID(segs[1]):
			g.device(w, r, who, segs[1], rest(escaped, 2))
		case apps.ValidID(first):
			g.shellPage(w, r, who, first)
		default:
			fail(w, http.StatusNotFound, "not found", g.shellHeaders)
		}
	})
}

// shellPage serves the shell's index.html for /, /<id> and /<id>/<route>.
func (g *Gateway) shellPage(w http.ResponseWriter, r *http.Request, who peer.WhoIs, id string) {
	if _, d := g.browserChecks(r, who, browserRoute{shellPage: true}); d != nil {
		failWith(w, d, g.shellHeaders, "GET, HEAD")
		return
	}
	if id != "" {
		if _, ok := g.cfg.Apps.Get(id); !ok {
			fail(w, http.StatusNotFound, "no app is installed at /"+id, g.shellHeaders)
			return
		}
	}
	g.shellHeaders(w.Header())
	g.serveFile(w, r, g.shellDir(), "/index.html", fileOpts{})
}

// shellFiles serves /lodge/<path> and /lodge/apps.
func (g *Gateway) shellFiles(w http.ResponseWriter, r *http.Request, who peer.WhoIs, segs []string) {
	if _, d := g.browserChecks(r, who, browserRoute{}); d != nil {
		failWith(w, d, g.shellHeaders, "GET, HEAD")
		return
	}
	g.shellHeaders(w.Header())
	if len(segs) == 2 && segs[1] == "apps" {
		g.appsList(w, r)
		return
	}
	rel := "/" + strings.Join(segs[1:], "/")
	g.serveFile(w, r, g.shellDir(), rel, fileOpts{shell: true, noFallback: true})
}

func (g *Gateway) shellDir() string {
	return path.Join(cmp.Or(g.cfg.Root, "/"), "usr/share/lodge/shell")
}

// railApp is one app in /lodge/apps.
type railApp struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Icon      string     `json:"icon"`
	Key       string     `json:"key"`
	Content   bool       `json:"content"`
	Companion bool       `json:"companion"`
	State     apps.State `json:"state"`
	Reason    string     `json:"reason"`
}

type rail struct {
	Contract      int       `json:"contract"`
	ContentOrigin string    `json:"contentOrigin"`
	Apps          []railApp `json:"apps"`
}

func (g *Gateway) appsList(w http.ResponseWriter, r *http.Request) {
	out := rail{Contract: Contract, ContentOrigin: g.cfg.Names.ContentOrigin, Apps: []railApp{}}
	for _, a := range g.cfg.Apps.List() {
		ra := railApp{ID: a.ID, Name: cmp.Or(a.Manifest.Name, a.ID), Key: a.Key, Content: a.Manifest.Content,
			Companion: a.Manifest.Companion, State: a.State, Reason: a.Reason}
		if a.Enabled() {
			ra.Icon = "/app/" + a.ID + "/" + a.Manifest.Icon
		}
		out.Apps = append(out.Apps, ra)
	}
	b, err := json.Marshal(out)
	if err != nil {
		fail(w, http.StatusInternalServerError, "lodge cannot list its apps", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(b)
}

// appRoute serves /app/<id>/…: the session, the app's x/ reads, or its
// files.
func (g *Gateway) appRoute(w http.ResponseWriter, r *http.Request, who peer.WhoIs, id, escaped string) {
	sub := rest(escaped, 2) // "/api", "/x/…", "/…" or ""
	isAPI := sub == "/api"
	a, installed := g.cfg.Apps.Get(id)
	flags := frameFlags{}
	if installed && a.Enabled() {
		flags = frameFlags{content: a.Manifest.Content, remoteImages: a.Manifest.RemoteImages}
	}
	headers := g.appHeaders(id, flags)
	p, d := g.browserChecks(r, who, browserRoute{api: isAPI})
	if d != nil {
		failWith(w, d, headers, map[bool]string{true: "GET", false: "GET, HEAD"}[isAPI])
		return
	}
	if !installed {
		fail(w, http.StatusNotFound, "no app is installed at /"+id, headers)
		return
	}
	if !a.Enabled() {
		fail(w, http.StatusServiceUnavailable, "app "+id+" is disabled: "+a.Reason, headers)
		return
	}
	switch {
	case isAPI:
		g.session(w, r, who, a, p, headers)
	case sub == "/x" || strings.HasPrefix(sub, "/x/"):
		if refuseUpgrade(w, r, headers) {
			return
		}
		g.proxy(w, r, proxyReq{app: a, kind: kindSession, peer: p, escapedPath: sub, headers: headers, modify: headers})
	default:
		headers(w.Header())
		g.serveFile(w, r, g.cfg.Apps.AppDir(id)+"/web", strings.TrimPrefix(r.URL.Path, "/app/"+id), fileOpts{fallback: true})
	}
}
