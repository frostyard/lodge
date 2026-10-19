// Package mcpsession binds an app's MCP sessions to the token that opened
// them (app-contract.md, MCP tokens and scope). It wraps any MCP
// Streamable HTTP handler that issues Mcp-Session-Id at initialize and ends
// a session on DELETE, such as the MCP SDK's StreamableHTTPHandler:
//
//   - a session is bound at initialize to the caller's Lodge-Token and
//     Lodge-Scope, and a request for it under another token name or scope
//     answers 404, as does one for a session it does not know;
//   - each token name keeps at most Max live sessions, and opening one
//     more ends the least recently used;
//   - a session idle for Idle ends: Sessions sweeps on every request and
//     every Every until Close, so an idle session ends, and frees what the
//     app holds for it, even when no other request comes.
//
// The binding keeps sessions apart; the scope itself is enforced by the
// app's engine, on the connection its tools use.
package mcpsession

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/frostyard/lodge/kit/lodgeapp"
)

// The defaults of Options.
const (
	DefaultMax   = 8
	DefaultIdle  = time.Hour
	DefaultEvery = time.Minute
)

// Header is MCP Streamable HTTP's session header.
const Header = "Mcp-Session-Id"

// Options configures Sessions. The zero value is the contract's defaults.
type Options struct {
	// Max bounds the live sessions per token name: DefaultMax when 0.
	Max int
	// Idle ends a session that has seen no request for this long:
	// DefaultIdle when 0.
	Idle time.Duration
	// CheckScope parses a scope at initialize. An error answers 403, as
	// the contract requires for a scope the app cannot parse. Nil admits
	// every scope.
	CheckScope func(scope string) error
	// Every is how often idle sessions are swept between requests:
	// DefaultEvery when 0.
	Every time.Duration
	// Now is the clock, for tests: time.Now when nil.
	Now func() time.Time
	// Logger receives ended sessions. Nil discards them.
	Logger *slog.Logger

	// ticks replaces the sweep's ticker, for tests.
	ticks <-chan time.Time
}

// Sessions is an http.Handler that binds next's MCP sessions.
type Sessions struct {
	next http.Handler
	opts Options

	mu       sync.Mutex
	sessions map[string]*session

	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

type session struct {
	id      string
	caller  lodgeapp.Caller // Token and Scope
	last    time.Time
	active  int // requests in flight
	removed bool
}

// New returns Sessions over next, and starts its sweeps; Close stops them.
func New(next http.Handler, opts Options) *Sessions {
	if opts.Max <= 0 {
		opts.Max = DefaultMax
	}
	if opts.Idle <= 0 {
		opts.Idle = DefaultIdle
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Every <= 0 {
		opts.Every = DefaultEvery
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	s := &Sessions{next: next, opts: opts, sessions: map[string]*session{},
		stop: make(chan struct{}), done: make(chan struct{})}
	go s.janitor()
	return s
}

// janitor sweeps every Every until Close.
func (s *Sessions) janitor() {
	defer close(s.done)
	ticks := s.opts.ticks
	if ticks == nil {
		t := time.NewTicker(s.opts.Every)
		defer t.Stop()
		ticks = t.C
	}
	for {
		select {
		case <-s.stop:
			return
		case <-ticks:
			s.sweep(context.Background())
		}
	}
}

// Close stops the sweeps between requests, and waits for one under way.
// Live sessions stay, and requests still pass; an app closes Sessions as
// it stops serving.
func (s *Sessions) Close() error {
	s.closeOnce.Do(func() { close(s.stop) })
	<-s.done
	return nil
}

// ServeHTTP implements http.Handler.
func (s *Sessions) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, ok := lodgeapp.CallerFrom(r.Context())
	if !ok || c.Kind != lodgeapp.Agent {
		http.Error(w, "MCP is for caller agent", http.StatusForbidden)
		return
	}
	s.sweep(r.Context())
	id := r.Header.Get(Header)
	if id == "" {
		s.open(w, r, c)
		return
	}
	ss := s.acquire(id, c)
	if ss == nil {
		http.Error(w, "no such MCP session", http.StatusNotFound)
		return
	}
	rw := &recorder{ResponseWriter: w}
	defer func() {
		ended := r.Method == http.MethodDelete || rw.code == http.StatusNotFound
		s.release(ss, ended)
	}()
	s.next.ServeHTTP(rw, r)
}

// open serves a request without a session: initialize, whose answer may
// name a new session.
func (s *Sessions) open(w http.ResponseWriter, r *http.Request, c lodgeapp.Caller) {
	if s.opts.CheckScope != nil {
		if err := s.opts.CheckScope(c.Scope); err != nil {
			http.Error(w, "the token's scope does not parse: "+err.Error(), http.StatusForbidden)
			return
		}
	}
	var ss, evict *session
	rw := &recorder{ResponseWriter: w, header: func(code int, h http.Header) {
		if id := h.Get(Header); id != "" && code/100 == 2 {
			ss, evict = s.bind(id, c)
		}
	}}
	s.next.ServeHTTP(rw, r)
	if ss != nil {
		s.release(ss, false)
	}
	// Past the bound, the least recently used session ends once
	// initialize has answered, outside next's own call.
	if evict != nil {
		s.end(r.Context(), evict, "its token opened too many sessions")
	}
}

// bind records a new session, in flight, and returns it with the least
// recently used of its token's sessions when they are now past Max, which
// it forgets.
func (s *Sessions) bind(id string, c lodgeapp.Caller) (ss, evict *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss = &session{id: id, caller: lodgeapp.Caller{Kind: lodgeapp.Agent, Token: c.Token, Scope: c.Scope},
		last: s.opts.Now(), active: 1}
	if old := s.sessions[id]; old != nil {
		old.removed = true
	}
	s.sessions[id] = ss
	n := 0
	for _, o := range s.sessions {
		if o.caller.Token != c.Token {
			continue
		}
		n++
		if o != ss && (evict == nil || older(o, evict)) {
			evict = o
		}
	}
	if n <= s.opts.Max {
		return ss, nil
	}
	s.remove(evict)
	return ss, evict
}

// older orders sessions for eviction: idle ones first, then the least
// recently used.
func older(a, b *session) bool {
	if (a.active == 0) != (b.active == 0) {
		return a.active == 0
	}
	return a.last.Before(b.last)
}

// acquire returns the live session id if c may use it, marking it in
// flight, or nil.
func (s *Sessions) acquire(id string, c lodgeapp.Caller) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessions[id]
	if ss == nil || ss.caller.Token != c.Token || ss.caller.Scope != c.Scope {
		return nil
	}
	ss.active++
	ss.last = s.opts.Now()
	return ss
}

// release ends a request on ss, and forgets ss when the request ended it.
func (s *Sessions) release(ss *session, ended bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss.active--
	ss.last = s.opts.Now()
	if ended {
		s.remove(ss)
	}
}

// remove forgets ss; s.mu is held.
func (s *Sessions) remove(ss *session) {
	if !ss.removed {
		ss.removed = true
		delete(s.sessions, ss.id)
	}
}

// sweep ends every session idle for Idle.
func (s *Sessions) sweep(ctx context.Context) {
	now := s.opts.Now()
	var idle []*session
	s.mu.Lock()
	for _, ss := range s.sessions {
		if ss.active == 0 && now.Sub(ss.last) >= s.opts.Idle {
			s.remove(ss)
			idle = append(idle, ss)
		}
	}
	s.mu.Unlock()
	for _, ss := range idle {
		s.end(ctx, ss, "idle")
	}
}

// end closes a forgotten session in next, as its client's DELETE would.
func (s *Sessions) end(ctx context.Context, ss *session, why string) {
	s.opts.Logger.Info("ending MCP session", "token", ss.caller.Token, "why", why)
	req, err := http.NewRequestWithContext(lodgeapp.WithCaller(context.WithoutCancel(ctx), ss.caller),
		http.MethodDelete, "/mcp", nil)
	if err != nil {
		return
	}
	req.Header.Set(Header, ss.id)
	s.next.ServeHTTP(discard{}, req)
}

// Len returns the number of live sessions, for tests.
func (s *Sessions) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// recorder notes a response's status, and shows header its headers before
// they are sent.
type recorder struct {
	http.ResponseWriter
	code   int
	header func(code int, h http.Header)
}

func (r *recorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
		if r.header != nil {
			r.header(code, r.Header())
		}
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.code == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(p)
}

// Flush keeps streams streaming.
func (r *recorder) Flush() {
	if r.code == 0 {
		r.WriteHeader(http.StatusOK)
	}
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the connection.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// discard is the ResponseWriter of a DELETE the client did not send.
type discard struct{}

func (discard) Header() http.Header         { return http.Header{} }
func (discard) Write(p []byte) (int, error) { return len(p), nil }
func (discard) WriteHeader(int)             {}
