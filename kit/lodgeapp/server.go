package lodgeapp

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/frostyard/lodge/kit/internal/wsline"
)

// MaxMessage is the largest /api frame: 16 MiB, one JSON-RPC message.
const MaxMessage = wsline.MaxMessage

// Health is GET /health's answer: {"ok": true, "version": "…", "protocol":
// N} when the app is up, or {"ok": false, "reason": "…"} while it is
// starting or degraded. lodge reads it with the same type.
type Health struct {
	OK bool `json:"ok"`
	// Version is the app's version.
	Version string `json:"version,omitzero"`
	// Protocol is the major of the app's own API, such as notes' RPC major.
	Protocol int `json:"protocol,omitzero"`
	// Reason says why the app is not OK.
	Reason string `json:"reason,omitzero"`
}

// Server serves an app's socket: every route of app-contract.md, each
// admitting its one caller kind. A request without Lodge-Caller, or with a
// kind its path does not admit, answers 403, except GET /health, which
// needs no caller. A route whose handler is nil answers 404.
//
// Handlers see the request as lodge sent it, path included, with its
// Caller in the context (CallerFrom).
type Server struct {
	// Health reports the app's state for GET /health. It is required.
	Health func(ctx context.Context) Health

	// API serves one browser session's GET /api WebSocket, for caller
	// session, until it returns. nc carries one JSON-RPC message per
	// line, as an app's Unix-socket server reads and writes them, so an
	// app hands it to the same server: frostnotes'
	// rpcserver.ServeConn(ctx, nc, router, caller). When the browser
	// leaves, lodge closes the socket and reads from nc fail; when ctx
	// ends, nc closes. API is required.
	API func(ctx context.Context, nc net.Conn, c Caller)

	// X serves GET and HEAD /x/<path>, the app's own reads for its UI,
	// for caller session.
	X http.Handler
	// Content serves GET and HEAD /content/<path>, untrusted bytes, for
	// caller content. Apps whose manifest sets content serve it.
	Content http.Handler
	// MCP serves POST, GET and DELETE /mcp, MCP Streamable HTTP, for
	// caller agent. Wrap it in mcpsession.
	MCP http.Handler
	// Device serves any method on /device/<path>, for caller device. The
	// app authenticates these requests itself.
	Device http.Handler

	// Logger receives refused connections and failed requests. Nil
	// discards them.
	Logger *slog.Logger
}

// Serve serves the socket until ctx ends, then closes every connection,
// waits for the API sessions to return, and returns nil.
func (s *Server) Serve(ctx context.Context, ln *Listener) error {
	if s.Health == nil || s.API == nil {
		return errors.New("lodgeapp: Server needs Health and API")
	}
	log := s.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// Every request's context, hijacked /api sessions' included, ends
	// with base.
	base, cancel := context.WithCancel(ctx)
	defer cancel()
	var sessions sync.WaitGroup
	defer sessions.Wait()
	srv := &http.Server{
		Handler:           &mux{s: s, log: log, sessions: &sessions},
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return base },
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelDebug),
		Protocols:         new(http.Protocols),
	}
	srv.Protocols.SetHTTP1(true)
	shut := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(shut)
		sctx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		_ = srv.Shutdown(sctx)
		_ = srv.Close()
	})
	err := srv.Serve(logged{ln, log})
	if stop() {
		// Serve failed on its own: ctx has not ended.
		_ = srv.Close()
		return err
	}
	<-shut
	return nil
}

// route is one row of the socket's table.
type route struct {
	kind    Kind
	methods []string // nil: any
	handler func(w http.ResponseWriter, r *http.Request, c Caller)
}

type mux struct {
	s        *Server
	log      *slog.Logger
	sessions *sync.WaitGroup
}

func (m *mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/health" {
		m.health(w, r)
		return
	}
	c, err := ParseCaller(r.Header)
	if err != nil {
		refuse(w, http.StatusForbidden, err.Error())
		return
	}
	if !clean(p) {
		refuse(w, http.StatusBadRequest, "the path is not clean")
		return
	}
	rt, ok := m.route(p)
	if !ok {
		refuse(w, http.StatusForbidden, "this path admits no caller")
		return
	}
	if c.Kind != rt.kind {
		refuse(w, http.StatusForbidden, "this path admits caller "+string(rt.kind)+", not "+string(c.Kind))
		return
	}
	if rt.handler == nil {
		refuse(w, http.StatusNotFound, "this app does not serve "+p)
		return
	}
	if rt.methods != nil && !slices.Contains(rt.methods, r.Method) {
		w.Header().Set("Allow", strings.Join(rt.methods, ", "))
		refuse(w, http.StatusMethodNotAllowed, r.Method+" is not allowed here")
		return
	}
	rt.handler(w, r.WithContext(WithCaller(r.Context(), c)), c)
}

func (m *mux) route(p string) (route, bool) {
	s := m.s
	serve := func(h http.Handler) func(http.ResponseWriter, *http.Request, Caller) {
		if h == nil {
			return nil
		}
		return func(w http.ResponseWriter, r *http.Request, _ Caller) { h.ServeHTTP(w, r) }
	}
	reads := []string{http.MethodGet, http.MethodHead}
	switch {
	case p == "/api":
		return route{Session, []string{http.MethodGet}, m.api}, true
	case strings.HasPrefix(p, "/x/"):
		return route{Session, reads, serve(s.X)}, true
	case strings.HasPrefix(p, "/content/"):
		return route{Content, reads, serve(s.Content)}, true
	case p == "/mcp":
		return route{Agent, []string{http.MethodPost, http.MethodGet, http.MethodDelete}, serve(s.MCP)}, true
	case strings.HasPrefix(p, "/device/"):
		return route{Device, nil, serve(s.Device)}, true
	}
	return route{}, false
}

func (m *mux) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		refuse(w, http.StatusMethodNotAllowed, r.Method+" is not allowed here")
		return
	}
	body, err := json.Marshal(m.s.Health(r.Context()))
	if err != nil {
		m.log.Error("health", "err", err)
		refuse(w, http.StatusInternalServerError, "health failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// api upgrades GET /api and hands the session to Server.API.
func (m *mux) api(w http.ResponseWriter, r *http.Request, c Caller) {
	if c.Session == "" {
		refuse(w, http.StatusForbidden, "no "+HeaderSession)
		return
	}
	// lodge has checked Origin, so the app must not (app-contract.md, /api),
	// and frames pass one to one, so nothing is compressed.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		m.log.Debug("api upgrade", "err", err)
		return // Accept has answered
	}
	m.sessions.Add(1)
	defer m.sessions.Done()
	nc := wsline.New(r.Context(), ws)
	defer func() { _ = nc.Close() }()
	m.s.API(r.Context(), nc, c)
}

// clean reports whether p is an absolute path that path.Clean keeps, but
// for a trailing slash.
func clean(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	q := strings.TrimSuffix(p, "/")
	return q == "" || path.Clean(q) == q
}

// refuse answers an error as the contract says: text/plain, one line.
func refuse(w http.ResponseWriter, code int, why string) {
	http.Error(w, why, code)
}
