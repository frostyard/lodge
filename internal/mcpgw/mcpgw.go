// Package mcpgw is lodge's MCP gateway, /gateway/mcp: 1 MCP server for the
// agents of every principal, on the Go MCP SDK, which only this package
// imports (docs/specs/lodge-http.md, The MCP gateway; ADR-0024).
//
// A principal's tools are, for each enabled app on hearth, each agent operation
// whose permission the principal's grant for that app holds, and lodge's own
// request_access. A call reads the grant again and sends 1 Connect call to the
// app's socket. Sessions are stateful, and each is bound to the OAuth client
// whose token opened it.
package mcpgw

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/oauth"
)

// Lifespans of the gateway's sessions and calls.
const (
	// DefaultSessionTimeout is how long a session with no request closes.
	DefaultSessionTimeout = time.Hour
	// DefaultCallTimeout bounds 1 tool call.
	DefaultCallTimeout = time.Minute
)

// Apps is what the gateway asks of the installed apps.
type Apps interface {
	// List returns every app, enabled or not.
	List() []apps.App
	// Get returns app id, and whether it is installed.
	Get(id string) (apps.App, bool)
}

// OAuth is what the gateway asks of lodge's OAuth server.
type OAuth interface {
	// Grant returns the principal's items for app.
	Grant(ctx context.Context, principal, app string) ([]oauth.Item, error)
	// RequestAccess makes an access request for client, and returns it.
	RequestAccess(ctx context.Context, client, app string, items []oauth.Item) (oauth.Request, error)
	// Live reports whether principal is live, and not revoked.
	Live(ctx context.Context, principal string) (bool, error)
}

// Options are what a Gateway needs.
type Options struct {
	// Apps are the installed apps.
	Apps Apps
	// OAuth is lodge's OAuth server: the grants, and the access requests.
	OAuth OAuth
	// Dial opens a connection to an app's socket [net.Dialer.DialContext]. It
	// is apps.Registry.DialContext in lodge, which checks the socket's owner.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Log is the log [discard].
	Log *slog.Logger
	// SessionTimeout is how long a session with no request closes
	// [DefaultSessionTimeout].
	SessionTimeout time.Duration
	// CallTimeout bounds 1 tool call [DefaultCallTimeout].
	CallTimeout time.Duration
	// Version is the server's version, which initialize reports.
	Version string
}

// Caller is who a request on the gateway is for: the principal, the client
// acting for it, and when its access token expires. lodge's checks set it, so
// the gateway trusts it.
type Caller struct {
	Principal string
	Client    string
	Expires   time.Time
}

// callerKey carries a Caller in a request's context.
type callerKey struct{}

// WithCaller returns ctx carrying c.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom returns the Caller of ctx, and whether ctx carries 1.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// Gateway is lodge's MCP gateway: 1 MCP server for each principal, and the
// Connect calls it makes to the apps.
type Gateway struct {
	opts   Options
	log    *slog.Logger
	client *http.Client
	// stream is the SDK's Streamable HTTP handler, behind its bearer check.
	stream http.Handler

	mu      sync.Mutex
	servers map[string]*server
	// cancels are the requests in flight for each client, so that a
	// revocation ends them at once.
	cancels map[string]map[*context.CancelFunc]struct{}
}

// New returns a gateway over o.
func New(o Options) *Gateway {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.SessionTimeout == 0 {
		o.SessionTimeout = DefaultSessionTimeout
	}
	if o.CallTimeout == 0 {
		o.CallTimeout = DefaultCallTimeout
	}
	if o.Version == "" {
		o.Version = "0"
	}
	if o.Dial == nil {
		d := &net.Dialer{Timeout: 10 * time.Second}
		o.Dial = d.DialContext
	}
	g := &Gateway{opts: o, log: o.Log, servers: map[string]*server{}, cancels: map[string]map[*context.CancelFunc]struct{}{}}
	g.client = &http.Client{Transport: &http.Transport{
		DialContext: o.Dial, MaxIdleConnsPerHost: 8, IdleConnTimeout: 30 * time.Second, DisableCompression: true,
	}}
	h := mcp.NewStreamableHTTPHandler(g.getServer, &mcp.StreamableHTTPOptions{
		SessionTimeout: o.SessionTimeout,
		// lodge checks Host itself, on the lodge node (docs/specs/lodge-http.md,
		// The MCP gateway).
		DisableLocalhostProtection: true,
		Logger:                     o.Log,
	})
	g.stream = auth.RequireBearerToken(g.verify, nil)(h)
	return g
}

// ServeHTTP serves 1 request on the gateway, after lodge's checks: the caller
// is in r's context.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, ok := CallerFrom(r.Context())
	if !ok {
		http.Error(w, "the request has no caller", http.StatusUnauthorized)
		return
	}
	// A request ends when its token expires, so a stream on GET ends then too.
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if c.Expires.IsZero() {
		ctx, cancel = context.WithCancel(r.Context())
	} else {
		ctx, cancel = context.WithDeadline(r.Context(), c.Expires)
	}
	defer cancel()
	defer g.register(c.Client, &cancel)()
	g.stream.ServeHTTP(w, r.WithContext(ctx))
}

// verify answers the SDK's bearer check. lodge has checked the token already,
// on the request's own context, so the token's text is not read again: its
// caller is the answer. A session is bound to the client the token names.
func (g *Gateway) verify(ctx context.Context, _ string, _ *http.Request) (*auth.TokenInfo, error) {
	c, ok := CallerFrom(ctx)
	if !ok {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{
		Scopes:     []string{oauth.ScopeMCP},
		Expiration: c.Expires,
		UserID:     c.Client,
		Extra:      map[string]any{"principal": c.Principal},
	}, nil
}

// getServer is the SDK's answer to a request's server: the principal's own.
func (g *Gateway) getServer(r *http.Request) *mcp.Server {
	c, ok := CallerFrom(r.Context())
	if !ok {
		return nil
	}
	return g.serverFor(r.Context(), c.Principal).srv
}

// server is 1 principal's MCP server, and its tools as they are registered.
type server struct {
	gw        *Gateway
	principal string
	srv       *mcp.Server
	// ready is closed once the server's first sync is done.
	ready chan struct{}

	mu sync.Mutex
	// tools maps a tool's name to a hash of its definition: a tool whose
	// definition is unchanged is not registered again, so no session gets a
	// notification for it.
	tools map[string]string
}

// serverFor returns principal's server, making it and its tools on first use.
func (g *Gateway) serverFor(ctx context.Context, principal string) *server {
	g.mu.Lock()
	s := g.servers[principal]
	g.mu.Unlock()
	if s != nil {
		// Another request may have made it a moment ago: wait for its tools.
		select {
		case <-s.ready:
		case <-ctx.Done():
		}
		return s
	}
	s = g.newServer(principal)
	g.mu.Lock()
	if old := g.servers[principal]; old != nil {
		g.mu.Unlock()
		select {
		case <-old.ready:
		case <-ctx.Done():
		}
		return old
	}
	g.servers[principal] = s
	g.mu.Unlock()
	if err := s.sync(ctx); err != nil {
		g.log.Warn("sync a new session's tools", "principal", principal, "err", err)
	}
	close(s.ready)
	return s
}

// newServer returns an empty server for principal: named lodge, with tools
// that declare a changed list.
func (g *Gateway) newServer(principal string) *server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "lodge", Version: g.opts.Version}, &mcp.ServerOptions{
		Logger: g.log,
		// The spec's server declares tools with listChanged: true, whatever
		// its tools are right now (docs/specs/lodge-http.md, The MCP gateway).
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
	})
	s := &server{gw: g, principal: principal, srv: srv, ready: make(chan struct{}), tools: map[string]string{}}
	// A call of a tool that this session's list does not hold still reaches
	// lodge's checks: the list says what the principal may call now, and the
	// call reads the grant again (ADR-0024, A call of <id>_<tool>).
	srv.AddReceivingMiddleware(s.unlistedCall)
	return s
}

// GrantChanged is oauth's OnGrant hook: a principal's grant for an app changed,
// so its tools are computed again. An unchanged tool sends no notification.
func (g *Gateway) GrantChanged(principal, app string) {
	g.mu.Lock()
	s := g.servers[principal]
	g.mu.Unlock()
	if s == nil {
		return
	}
	ctx, cancel := g.background()
	defer cancel()
	if err := s.sync(ctx); err != nil {
		g.log.Warn("sync a principal's tools after a grant change", "principal", principal, "app", app, "err", err)
	}
}

// AppsChanged is called after a reload: every principal's tools are computed
// again, so a new app's tools reach open sessions and a removed app's do not.
func (g *Gateway) AppsChanged() {
	g.mu.Lock()
	servers := make([]*server, 0, len(g.servers))
	for _, s := range g.servers {
		servers = append(servers, s)
	}
	g.mu.Unlock()
	if len(servers) == 0 {
		return
	}
	ctx, cancel := g.background()
	defer cancel()
	for _, s := range servers {
		if err := s.sync(ctx); err != nil {
			g.log.Warn("sync a principal's tools after a reload", "principal", s.principal, "err", err)
		}
	}
}

// Revoked is oauth's OnRevoke hook: every request and stream of those clients
// ends at once, and a revoked principal's server goes with its sessions.
func (g *Gateway) Revoked(principal string, clients []string) {
	g.mu.Lock()
	var ends []*context.CancelFunc
	for _, c := range clients {
		for cancel := range g.cancels[c] {
			ends = append(ends, cancel)
		}
		delete(g.cancels, c)
	}
	g.mu.Unlock()
	for _, cancel := range ends {
		(*cancel)()
	}
	ctx, cancel := g.background()
	defer cancel()
	live, err := g.opts.OAuth.Live(ctx, principal)
	if err != nil {
		g.log.Warn("read a principal after a revocation", "principal", principal, "err", err)
		return
	}
	if live {
		g.log.Info("revoked a client's requests", "principal", principal, "clients", len(clients), "requests", len(ends))
		return
	}
	g.mu.Lock()
	s := g.servers[principal]
	delete(g.servers, principal)
	g.mu.Unlock()
	if s == nil {
		return
	}
	for session := range s.srv.Sessions() {
		_ = session.Close()
	}
	g.log.Info("revoked a principal's sessions", "principal", principal, "clients", len(clients), "requests", len(ends))
}

// background is the context of a hook's sync: it outlives the request that
// caused it.
func (g *Gateway) background() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// register records a request's cancel under its client, and returns the
// function that forgets it.
func (g *Gateway) register(client string, cancel *context.CancelFunc) func() {
	g.mu.Lock()
	if g.cancels[client] == nil {
		g.cancels[client] = map[*context.CancelFunc]struct{}{}
	}
	g.cancels[client][cancel] = struct{}{}
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		delete(g.cancels[client], cancel)
		if len(g.cancels[client]) == 0 {
			delete(g.cancels, client)
		}
	}
}

// GrantHeader is a grant's header value: the items' JSON, in base64url without
// padding, as hearth's adapter reads it (app-contract.md, Caller headers).
func GrantHeader(items []oauth.Item) (string, error) {
	b, err := canonicalJSON(items)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// canonicalJSON is v in lodge's canonical, deterministic JSON.
func canonicalJSON(v any) ([]byte, error) { return json.Marshal(v, json.Deterministic(true)) }
