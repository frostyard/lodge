package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/frostyard/lodge/internal/agents"
	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/peer"
	"github.com/frostyard/lodge/internal/tokens"
)

// Content serves the content node's routes, naming peers with who
// (docs/specs/lodge-http.md, Content routes).
func (g *Gateway) Content(who peer.WhoIs) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escaped := r.URL.EscapedPath()
		segs, ok := segments(escaped)
		id := ""
		if ok && len(segs) >= 2 {
			id = segs[0]
		}
		a, installed := g.cfg.Apps.Get(id)
		headers := g.contentHeaders(installed && a.Manifest.ContentLinks)
		// 1. Host.
		if !g.Names().contentHost(r.Host) {
			fail(w, http.StatusForbidden, "this is not lodge's content address", headers)
			return
		}
		// 2. Method.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			fail(w, http.StatusMethodNotAllowed, "method "+r.Method+" is not allowed here", headers)
			return
		}
		// 3. Fetch metadata, when present.
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-site", "same-origin", "none":
		default:
			fail(w, http.StatusForbidden, "requests from another site are refused", headers)
			return
		}
		// 4. Identity.
		p, d := g.identity(r, who)
		if d != nil {
			fail(w, d.status, d.line, headers)
			return
		}
		// 5. App.
		switch {
		case id == "" || !installed:
			fail(w, http.StatusNotFound, "not found", headers)
			return
		case !a.Enabled():
			fail(w, http.StatusServiceUnavailable, "app "+id+" is disabled: "+a.Reason, headers)
			return
		case !a.Manifest.Content:
			fail(w, http.StatusNotFound, "not found", headers)
			return
		}
		if refuseUpgrade(w, r, headers) {
			return
		}
		g.proxy(w, r, proxyReq{app: a, kind: kindContent, peer: p, escapedPath: "/content" + rest(escaped, 1),
			headers: headers, modify: contentModify(headers)})
	})
}

// device serves /device/<id>/<path>: lodge checks no identity, and the app
// authenticates the request (docs/specs/lodge-http.md, Device routes).
func (g *Gateway) device(w http.ResponseWriter, r *http.Request, who peer.WhoIs, id, sub string) {
	if !g.Names().lodgeHost(r.Host) {
		fail(w, http.StatusForbidden, "this is not lodge's address", deviceHeaders)
		return
	}
	a, ok := g.cfg.Apps.Get(id)
	switch {
	case !ok || (a.Enabled() && !a.Manifest.Device):
		fail(w, http.StatusNotFound, "not found", deviceHeaders)
		return
	case !a.Enabled():
		fail(w, http.StatusServiceUnavailable, "app "+id+" is disabled: "+a.Reason, deviceHeaders)
		return
	}
	// The peer is named for display only, and only from one of its own
	// addresses, as for every other kind.
	p, err := g.whoIs(r, who)
	named := err == nil
	if ip, ok := remoteIP(r.RemoteAddr); named && (!ok || !p.Owns(ip)) {
		named = false
	}
	req := proxyReq{app: a, kind: kindDevice, peer: p, named: named, escapedPath: "/device" + sub,
		headers: deviceHeaders, modify: deviceHeaders}
	if isWebSocket(r) {
		g.deviceSocket(w, r, req)
		return
	}
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = &deadlineBody{ReadCloser: r.Body, rc: http.NewResponseController(w), timeout: g.cfg.BodyTimeout}
	}
	g.proxy(w, r, req)
}

// deadlineBody gives each read of a request body its own deadline, so a
// body may be long but not stalled. The deadline is lifted at its end.
type deadlineBody struct {
	io.ReadCloser
	rc      *http.ResponseController
	timeout time.Duration
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	_ = b.rc.SetReadDeadline(time.Now().Add(b.timeout))
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		_ = b.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}

// deviceSocket relays a device's WebSocket to the app as a session's is,
// with the device's own headers and no liveness rule.
func (g *Gateway) deviceSocket(w http.ResponseWriter, r *http.Request, p proxyReq) {
	h := r.Header.Clone()
	for _, k := range []string{"Connection", "Upgrade", "Sec-Websocket-Key", "Sec-Websocket-Version",
		"Sec-Websocket-Extensions", "Sec-Websocket-Protocol", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding",
		"Proxy-Connection", "Proxy-Authorization", "Host"} {
		h.Del(k)
	}
	stripClient(h, true)
	setCaller(h, p)
	var subprotocols []string
	for _, v := range r.Header.Values("Sec-Websocket-Protocol") {
		for part := range strings.SplitSeq(v, ",") {
			if s := strings.TrimSpace(part); s != "" {
				subprotocols = append(subprotocols, s)
			}
		}
	}
	path := p.escapedPath
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	app, err := g.dialApp(r.Context(), p.app.ID, path, h, subprotocols)
	if err != nil {
		g.log.Warn("open a device's socket", "app", p.app.ID, "err", err)
		fail(w, http.StatusBadGateway, "app "+p.app.ID+" did not accept the socket", deviceHeaders)
		return
	}
	deviceHeaders(w.Header())
	opts := &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled}
	if sp := app.Subprotocol(); sp != "" {
		opts.Subprotocols = []string{sp}
	}
	browser, err := websocket.Accept(w, r, opts)
	if err != nil {
		app.Close(websocket.StatusGoingAway, "the device did not upgrade")
		return
	}
	relay(r.Context(), browser, app, nil, false)
}

// streams are the requests lodge is proxying for each MCP token, by the
// token's row, so that revoking it closes them at once.
type streams struct {
	mu      sync.Mutex
	byToken map[int64]map[*context.CancelFunc]struct{}
}

func newStreams() *streams {
	return &streams{byToken: map[int64]map[*context.CancelFunc]struct{}{}}
}

func (s *streams) add(name int64, cancel context.CancelFunc) (remove func()) {
	key := &cancel
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byToken[name] == nil {
		s.byToken[name] = map[*context.CancelFunc]struct{}{}
	}
	s.byToken[name][key] = struct{}{}
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.byToken[name], key)
		if len(s.byToken[name]) == 0 {
			delete(s.byToken, name)
		}
	}
}

func (s *streams) end(name int64) int {
	s.mu.Lock()
	cancels := s.byToken[name]
	delete(s.byToken, name)
	s.mu.Unlock()
	for c := range cancels {
		(*c)()
	}
	return len(cancels)
}

// count is the number of requests in flight for the token.
func (s *streams) count(name int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byToken[name])
}

// EndToken closes every request and stream lodge is proxying for the
// token with row id, after it is revoked.
func (g *Gateway) EndToken(id int64) int { return g.streams.end(id) }

// refuseUpgrade answers 400 to an upgrade on a route that takes none:
// only sessions and device routes relay WebSockets.
func refuseUpgrade(w http.ResponseWriter, r *http.Request, headers func(http.Header)) bool {
	if r.Header.Get("Upgrade") == "" && !headerHasToken(r.Header, "Connection", "upgrade") {
		return false
	}
	fail(w, http.StatusBadRequest, "this route takes no upgrade", headers)
	return true
}

// statusWriter records the status an MCP response sends, for the log.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// mcp serves /mcp/<id> on the lodge node (docs/specs/lodge-http.md,
// /mcp/<id>).
func (g *Gateway) mcp(w http.ResponseWriter, r *http.Request, who peer.WhoIs, id string) {
	g.serveMCP(w, r, mcpRoute{who: who}, id)
}

// Agent serves the agent socket: /mcp/<id> alone, for the agent its
// connection was admitted as (docs/specs/lodge-http.md, The agent socket).
// The connection's user stands in for Host, WhoIs and the address check.
func (g *Gateway) Agent() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := agents.User(r.Context())
		if !ok {
			fail(w, http.StatusForbidden, "lodge cannot name this agent", agentHeaders)
			return
		}
		segs, ok := segments(r.URL.EscapedPath())
		if !ok || r.URL.Opaque != "" || len(segs) != 2 || segs[0] != "mcp" || !apps.ValidID(segs[1]) {
			fail(w, http.StatusNotFound, "not found", agentHeaders)
			return
		}
		g.serveMCP(w, r, mcpRoute{agent: user}, segs[1])
	})
}

// mcpRoute is where an MCP request came: the lodge node, whose peers who
// names, or the agent socket, from agent's user.
type mcpRoute struct {
	who   peer.WhoIs
	agent string
}

func (g *Gateway) serveMCP(w http.ResponseWriter, r *http.Request, route mcpRoute, id string) {
	sw := &statusWriter{ResponseWriter: w}
	var p peer.Peer
	var tok tokens.Token
	defer func() {
		attrs := []any{"app", id, "node", p.NodeName}
		if route.agent != "" {
			attrs = []any{"app", id, "agent", route.agent}
		}
		g.log.Info("mcp", append(attrs, "token", tok.Name, "method", r.Method, "status", sw.status)...)
	}()
	w = sw
	onSocket := route.agent != ""
	// 1. Host, on the tailnet.
	if !onSocket && !g.Names().lodgeHost(r.Host) {
		fail(w, http.StatusForbidden, "this is not lodge's address", agentHeaders)
		return
	}
	// 2. No Origin.
	if _, ok := r.Header["Origin"]; ok {
		fail(w, http.StatusForbidden, "browsers may not call /mcp", agentHeaders)
		return
	}
	// 3. Method.
	switch r.Method {
	case http.MethodPost, http.MethodGet, http.MethodDelete:
	default:
		w.Header().Set("Allow", "POST, GET, DELETE")
		fail(w, http.StatusMethodNotAllowed, "method "+r.Method+" is not allowed here", agentHeaders)
		return
	}
	// 4. Peer, on the tailnet: checks a and b.
	if !onSocket {
		var d *denial
		if p, d = g.peerChecks(r, route.who); d != nil {
			fail(w, d.status, d.line, agentHeaders)
			return
		}
	}
	// 5. Token, for this app and this route.
	unauthorized := func() {
		w.Header().Set("WWW-Authenticate", "Bearer")
		fail(w, http.StatusUnauthorized, "unauthorized", agentHeaders)
	}
	t, err := g.cfg.Tokens.Check(r.Context(), tokens.Bearer(r.Header.Get("Authorization")), id, route.agent)
	if err != nil {
		unauthorized()
		return
	}
	tok = t
	// 6. App.
	a, ok := g.cfg.Apps.Get(id)
	switch {
	case !ok || (a.Enabled() && !a.Manifest.MCP):
		fail(w, http.StatusNotFound, "not found", agentHeaders)
		return
	case !a.Enabled():
		fail(w, http.StatusServiceUnavailable, "app "+id+" is disabled: "+a.Reason, agentHeaders)
		return
	}
	if refuseUpgrade(w, r, agentHeaders) {
		return
	}
	// Register the stream under the token's row, then check the secret
	// again: a revocation either sees this request or is seen by it, even
	// when a new token takes the revoked one's name in between.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer g.streams.add(tok.ID, cancel)()
	if again, err := g.cfg.Tokens.Check(ctx, tokens.Bearer(r.Header.Get("Authorization")), id, route.agent); err != nil || again.ID != tok.ID {
		unauthorized()
		return
	}
	if err := g.cfg.Tokens.Touch(ctx, tok.Name); err != nil {
		g.log.Warn("record a token's use", "token", tok.Name, "err", err)
	}
	g.proxy(w, r.WithContext(ctx), proxyReq{app: a, kind: kindAgent, peer: p, token: tok, escapedPath: "/mcp",
		headers: agentHeaders, modify: agentHeaders})
}
