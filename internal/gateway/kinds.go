package gateway

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"

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
		if !g.cfg.Names.contentHost(r.Host) {
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
		g.proxy(w, r, proxyReq{app: a, kind: kindContent, peer: p, escapedPath: "/content" + rest(escaped, 1),
			headers: headers, modify: contentModify(headers)})
	})
}

// device serves /device/<id>/<path>: lodge checks no identity, and the app
// authenticates the request (docs/specs/lodge-http.md, Device routes).
func (g *Gateway) device(w http.ResponseWriter, r *http.Request, who peer.WhoIs, id, sub string) {
	if !g.cfg.Names.lodgeHost(r.Host) {
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
	p, err := g.whoIs(r, who)
	req := proxyReq{app: a, kind: kindDevice, peer: p, named: err == nil, escapedPath: "/device" + sub,
		headers: deviceHeaders, modify: deviceHeaders}
	if isWebSocket(r) {
		g.deviceSocket(w, r, req)
		return
	}
	g.proxy(w, r, req)
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

// streams are the requests lodge is proxying for each MCP token, so that
// revoking it closes them at once.
type streams struct {
	mu      sync.Mutex
	byToken map[string]map[*context.CancelFunc]struct{}
}

func newStreams() *streams {
	return &streams{byToken: map[string]map[*context.CancelFunc]struct{}{}}
}

func (s *streams) add(name string, cancel context.CancelFunc) (remove func()) {
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

func (s *streams) end(name string) int {
	s.mu.Lock()
	cancels := s.byToken[name]
	delete(s.byToken, name)
	s.mu.Unlock()
	for c := range cancels {
		(*c)()
	}
	return len(cancels)
}

// count is the number of requests in flight for name.
func (s *streams) count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byToken[name])
}

// EndToken closes every request and stream lodge is proxying for the
// token name, after it is revoked.
func (g *Gateway) EndToken(name string) int { return g.streams.end(name) }

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

// mcp serves /mcp/<id> (docs/specs/lodge-http.md, /mcp/<id>).
func (g *Gateway) mcp(w http.ResponseWriter, r *http.Request, who peer.WhoIs, id string) {
	sw := &statusWriter{ResponseWriter: w}
	var p peer.Peer
	var tok tokens.Token
	defer func() {
		g.log.Info("mcp", "app", id, "node", p.NodeName, "token", tok.Name, "method", r.Method, "status", sw.status)
	}()
	w = sw
	// 1. Host.
	if !g.cfg.Names.lodgeHost(r.Host) {
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
	// 4. Peer: checks a and b.
	var d *denial
	if p, d = g.peerChecks(r, who); d != nil {
		fail(w, d.status, d.line, agentHeaders)
		return
	}
	// 5. Token.
	unauthorized := func() {
		w.Header().Set("WWW-Authenticate", "Bearer")
		fail(w, http.StatusUnauthorized, "unauthorized", agentHeaders)
	}
	t, err := g.cfg.Tokens.Check(r.Context(), tokens.Bearer(r.Header.Get("Authorization")), id)
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
	// Register the stream, then check the token again: a revocation either
	// sees this request or is seen by it.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer g.streams.add(tok.Name, cancel)()
	if live, err := g.cfg.Tokens.Live(ctx, tok.Name); err != nil || !live {
		unauthorized()
		return
	}
	if err := g.cfg.Tokens.Touch(ctx, tok.Name); err != nil {
		g.log.Warn("record a token's use", "token", tok.Name, "err", err)
	}
	g.proxy(w, r.WithContext(ctx), proxyReq{app: a, kind: kindAgent, peer: p, token: tok, escapedPath: "/mcp",
		headers: agentHeaders, modify: agentHeaders})
}
