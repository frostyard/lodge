// Package inference serves lodge's inference socket,
// /run/lodge/inference.sock (docs/specs/inference.md; app-contract.md,
// Inference): an OpenAI-compatible API for app daemons, which ask for
// aliases. lodge names the calling app by the socket peer's uid, queues
// each request for its provider's slots, interactive first, forwards it
// with the provider's key, and logs its usage, never its content.
package inference

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Limits (docs/specs/inference.md).
const (
	MaxBody      = 32 << 20
	CheckEvery   = time.Minute
	CheckTimeout = 5 * time.Second
)

// Callers names the app a socket peer is.
type Callers interface {
	// AppForUID is the enabled app whose manifest's user has uid.
	AppForUID(uid int) (string, bool)
	// AppEnabled reports whether id is an enabled app.
	AppEnabled(id string) bool
}

// Options are what the server needs.
type Options struct {
	DB      *sql.DB
	Callers Callers
	// DevUID, under a layout root, is lodge's own uid, whose requests name
	// their app in Lodge-App; -1 otherwise.
	DevUID int
	// Client reaches providers [a client with no overall timeout].
	Client *http.Client
	Log    *slog.Logger     // [discard]
	Now    func() time.Time // [time.Now]
}

// Provider is one configured provider with its queue and availability.
type Provider struct {
	Name  string
	Cfg   ProviderConfig
	Queue *Queue

	mu        sync.Mutex
	available bool
	reason    string
	// checked is closed once the provider has been checked.
	checked chan struct{}
	once    *sync.Once
}

// waitChecked waits, at most CheckTimeout and a second, for the provider's
// first check, so a request right after start is not refused for it.
func (p *Provider) waitChecked(ctx context.Context) {
	t := time.NewTimer(CheckTimeout + time.Second)
	defer t.Stop()
	select {
	case <-p.checked:
	case <-ctx.Done():
	case <-t.C:
	}
}

func (p *Provider) state() (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.available, p.reason
}

func (p *Provider) set(ok bool, reason string) (changed bool) {
	p.mu.Lock()
	changed = p.available != ok || p.reason != reason
	p.available, p.reason = ok, reason
	p.mu.Unlock()
	p.once.Do(func() { close(p.checked) })
	return changed
}

type state struct {
	cfg       Config
	err       error // why the configuration was refused
	providers map[string]*Provider
}

// Server serves the inference socket.
type Server struct {
	db      *sql.DB
	callers Callers
	devUID  int
	client  *http.Client
	log     *slog.Logger
	now     func() time.Time

	cur   atomic.Pointer[state]
	check chan struct{}
}

// New returns a server with no configuration until Load.
func New(o Options) *Server {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Client == nil {
		o.Client = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			ResponseHeaderTimeout: 10 * time.Minute,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
		}}
	}
	s := &Server{db: o.DB, callers: o.Callers, devUID: o.DevUID, client: o.Client, log: o.Log, now: o.Now,
		check: make(chan struct{}, 1)}
	s.cur.Store(&state{providers: map[string]*Provider{}})
	return s
}

func (s *Server) state() *state { return s.cur.Load() }

// Load replaces the configuration. err, from LoadConfig, refuses it whole:
// lodge then has no aliases. A provider that stays with its slots keeps its
// queue, so requests in flight count.
func (s *Server) Load(cfg Config, err error) {
	old := s.state()
	next := &state{providers: map[string]*Provider{}}
	if err != nil {
		next.err = err
		s.log.Error("inference configuration refused", "err", err)
	} else {
		next.cfg = cfg
		for name, pc := range cfg.Providers {
			p := &Provider{Name: name, Cfg: pc}
			if o, ok := old.providers[name]; ok && o.Cfg == pc {
				p.Queue, p.checked, p.once = o.Queue, o.checked, o.once
				p.available, p.reason = o.state()
			} else {
				p.Queue = NewQueue(max(pc.Slots, 1))
				p.reason = "not checked yet"
				p.checked, p.once = make(chan struct{}), new(sync.Once)
			}
			if pc.Type != TypeOpenAI {
				p.available, p.reason = false, "not built: its terms are to verify (ADR-0011)"
				p.once.Do(func() { close(p.checked) })
			}
			next.providers[name] = p
		}
	}
	s.cur.Store(next)
	s.CheckSoon()
}

// CheckSoon asks Run to check every provider now.
func (s *Server) CheckSoon() {
	select {
	case s.check <- struct{}{}:
	default:
	}
}

// Run checks every provider at once, every CheckEvery, and when asked,
// until ctx ends.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(CheckEvery)
	defer t.Stop()
	for {
		s.CheckAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.check:
		}
	}
}

// CheckAll checks every openai provider's reachability.
func (s *Server) CheckAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, p := range s.state().providers {
		if p.Cfg.Type != TypeOpenAI {
			continue
		}
		wg.Go(func() {
			ok, reason := s.probe(ctx, p)
			if p.set(ok, reason) {
				if ok {
					s.log.Info("inference provider available", "provider", p.Name)
				} else {
					s.log.Warn("inference provider unavailable", "provider", p.Name, "reason", reason)
				}
			}
		})
	}
	wg.Wait()
}

// probe asks GET <baseURL>/models, with the key.
func (s *Server) probe(ctx context.Context, p *Provider) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Cfg.BaseURL+"/models", nil)
	if err != nil {
		return false, err.Error()
	}
	if k := s.key(ctx, p.Name); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false, "cannot be reached"
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return false, fmt.Sprintf("GET /models answered %d", resp.StatusCode)
	}
	return true, ""
}

// ProviderStatus is a provider for lodgectl status.
type ProviderStatus struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitzero"`
	Slots     int    `json:"slots"`
	Load
}

// Status lists the providers, and why the configuration was refused.
func (s *Server) Status() ([]ProviderStatus, string) {
	st := s.state()
	out := []ProviderStatus{}
	for _, name := range slices.Sorted(maps.Keys(st.providers)) {
		p := st.providers[name]
		ok, reason := p.state()
		out = append(out, ProviderStatus{Name: name, Type: p.Cfg.Type, Available: ok, Reason: reason, Slots: p.Cfg.Slots, Load: p.Queue.Load()})
	}
	refused := ""
	if st.err != nil {
		refused = st.err.Error()
	}
	return out, refused
}

// --- The socket ---

// Listen makes the inference socket at path, mode 0660, in group (when
// group is not empty), replacing a stale one.
func Listen(path, group string) (*net.UnixListener, error) {
	if len(path) > 107 {
		return nil, fmt.Errorf("inference socket %s: longer than a Unix socket's 107 bytes", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("inference socket: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inference socket: %w", err)
	}
	old := syscall.Umask(0o177)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	syscall.Umask(old)
	if err != nil {
		return nil, fmt.Errorf("inference socket: %w", err)
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			ln.Close()
			return nil, fmt.Errorf("inference socket: group %s: %w", group, err)
		}
		gid, _ := strconv.Atoi(g.Gid)
		if err := os.Chown(path, -1, gid); err != nil {
			ln.Close()
			return nil, fmt.Errorf("inference socket: %w (lodge must be in group %s)", err, group)
		}
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, fmt.Errorf("inference socket: %w", err)
	}
	return ln, nil
}

type uidKey struct{}

// connUID records each connection's peer uid in its context.
func connUID(ctx context.Context, c net.Conn) context.Context {
	uid := -1
	if uc, ok := c.(*net.UnixConn); ok {
		if raw, err := uc.SyscallConn(); err == nil {
			_ = raw.Control(func(fd uintptr) {
				if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil {
					uid = int(cred.Uid)
				}
			})
		}
	}
	return context.WithValue(ctx, uidKey{}, uid)
}

// Serve serves ln until ctx ends.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext:       connUID,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
		Protocols:         new(http.Protocols),
	}
	srv.Protocols.SetHTTP1(true)
	stop := context.AfterFunc(ctx, func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		_ = srv.Close()
	})
	defer stop()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// --- Requests ---

func apiError(w http.ResponseWriter, status int, typ, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": code}})
}

// caller names the request's app by its connection's peer uid.
func (s *Server) caller(r *http.Request) (string, bool) {
	uid, _ := r.Context().Value(uidKey{}).(int)
	if uid < 0 || s.callers == nil {
		return "", false
	}
	if s.devUID >= 0 && uid == s.devUID {
		id := r.Header.Get("Lodge-App")
		return id, id != "" && s.callers.AppEnabled(id)
	}
	return s.callers.AppForUID(uid)
}

// Handler serves the API.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		app, ok := s.caller(r)
		if !ok {
			apiError(w, http.StatusForbidden, "permission_error", "unknown_caller", "this socket's peer is no enabled app")
			s.record(r.Context(), Record{At: start, Status: http.StatusForbidden, Input: -1, Output: -1, FirstByte: -1})
			return
		}
		var endpoint string
		switch r.URL.Path {
		case "/v1/models":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				apiError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "GET only")
				return
			}
			s.models(r.Context(), w, app)
			return
		case "/v1/chat/completions":
			endpoint = "/chat/completions"
		case "/v1/responses":
			endpoint = "/responses"
		default:
			apiError(w, http.StatusNotFound, "invalid_request_error", "not_found", "no such endpoint")
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			apiError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "POST only")
			return
		}
		s.forward(w, r, app, endpoint, start)
	})
}

type modelEntry struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	OwnedBy   string `json:"owned_by"`
	Available bool   `json:"available"`
}

func (s *Server) models(ctx context.Context, w http.ResponseWriter, app string) {
	st := s.state()
	out := struct {
		Object string       `json:"object"`
		Data   []modelEntry `json:"data"`
	}{Object: "list", Data: []modelEntry{}}
	for _, alias := range Aliases {
		if !st.cfg.Allowed(app, alias) {
			continue
		}
		ok := false
		if p := st.providers[st.cfg.Aliases[alias].Provider]; p != nil {
			p.waitChecked(ctx)
			ok, _ = p.state()
		}
		out.Data = append(out.Data, modelEntry{ID: alias, Object: "model", OwnedBy: "lodge", Available: ok})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, out)
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, app, endpoint string, start time.Time) {
	rec := Record{At: start, App: app, Priority: ParsePriority(r.Header.Get("Lodge-Priority")), Input: -1, Output: -1, FirstByte: -1}
	done := func(status int) {
		rec.Status = status
		rec.Total = s.now().Sub(start)
		s.record(r.Context(), rec)
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		apiError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "body_too_large", "the body is over 32 MiB")
		done(http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		return // the app went away
	}
	var members map[string]jsontext.Value
	var alias string
	var stream bool
	if json.Unmarshal(body, &members) != nil || json.Unmarshal(members["model"], &alias) != nil {
		apiError(w, http.StatusBadRequest, "invalid_request_error", "invalid_body", "the body must be a JSON object with a string model")
		done(http.StatusBadRequest)
		return
	}
	if v, ok := members["stream"]; ok {
		_ = json.Unmarshal(v, &stream)
	}
	rec.Alias = alias
	st := s.state()
	if !st.cfg.Allowed(app, alias) {
		apiError(w, http.StatusNotFound, "invalid_request_error", "model_not_found",
			fmt.Sprintf("the model %q does not exist or is not allowed for app %s", alias, app))
		done(http.StatusNotFound)
		return
	}
	ac := st.cfg.Aliases[alias]
	p := st.providers[ac.Provider]
	rec.Provider, rec.Model = p.Name, ac.Model
	p.waitChecked(r.Context())
	if ok, reason := p.state(); !ok {
		apiError(w, http.StatusServiceUnavailable, "server_error", "provider_unavailable",
			fmt.Sprintf("provider %s for %s is unavailable: %s", p.Name, alias, reason))
		done(http.StatusServiceUnavailable)
		return
	}
	model, _ := json.Marshal(ac.Model)
	members["model"] = model
	out, err := json.Marshal(members)
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid_request_error", "invalid_body", "the body cannot be re-encoded")
		done(http.StatusBadRequest)
		return
	}

	release, err := p.Queue.Acquire(r.Context(), rec.Priority)
	rec.Wait = s.now().Sub(start)
	if errors.Is(err, ErrQueueFull) {
		apiError(w, http.StatusTooManyRequests, "rate_limit_error", "queue_full", "too many requests wait for "+p.Name)
		done(http.StatusTooManyRequests)
		return
	}
	if err != nil {
		done(499) // the app went away while it waited
		return
	}
	defer release()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.Cfg.BaseURL+endpoint, bytes.NewReader(out))
	if err != nil {
		apiError(w, http.StatusBadGateway, "server_error", "provider_failed", "lodge cannot build the provider's request")
		done(http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", cmp.Or(r.Header.Get("Accept"), "application/json"))
	if k := s.key(r.Context(), p.Name); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	sent := s.now()
	resp, err := s.client.Do(req)
	if err != nil {
		if r.Context().Err() != nil {
			done(499)
			return
		}
		s.log.Warn("inference provider failed", "provider", p.Name, "err", err)
		p.set(false, "a request could not reach it")
		s.CheckSoon()
		apiError(w, http.StatusBadGateway, "server_error", "provider_failed", "provider "+p.Name+" could not be reached")
		done(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, k := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	tap := newTap(stream)
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if rec.FirstByte < 0 {
				rec.FirstByte = s.now().Sub(sent)
			}
			tap.Write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			_ = rc.Flush()
		}
		if err != nil {
			break
		}
	}
	tap.Finish()
	rec.Input, rec.Output = tap.in, tap.out
	done(resp.StatusCode)
}
