package gateway

import (
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/mcpgw"
	"github.com/frostyard/lodge/internal/oauth"
	"github.com/frostyard/lodge/internal/peer"
	"github.com/frostyard/lodge/internal/tokens"
)

// gatewayPath is the MCP gateway's path on the lodge node.
const gatewayPath = "/gateway/mcp"

// gatewayHeaders sets the confined set of the gateway's responses, without
// Cross-Origin-Resource-Policy and with Cache-Control left to the MCP SDK
// (docs/specs/lodge-http.md, The MCP gateway).
func gatewayHeaders(h http.Header) {
	apartHeaders(h)
	h.Del("Cross-Origin-Resource-Policy")
	dropCredentialsAndCORS(h)
}

// gatewayMCP serves /gateway/mcp: lodge's own MCP server for every principal
// (docs/specs/lodge-http.md, The MCP gateway; ADR-0024).
func (g *Gateway) gatewayMCP(w http.ResponseWriter, r *http.Request, who peer.WhoIs) {
	sw := &statusWriter{ResponseWriter: w}
	var p peer.Peer
	var caller mcpgw.Caller
	defer func() {
		g.log.Info("mcp gateway", "node", p.NodeName, "principal", caller.Principal, "client", caller.Client,
			"method", r.Method, "status", sw.status)
	}()
	w = sw
	// It is 404 with no OAuth server.
	if g.cfg.MCP == nil || g.cfg.OAuth == nil {
		fail(w, http.StatusNotFound, "not found", gatewayHeaders)
		return
	}
	// 1. Host.
	if !g.Names().lodgeHost(r.Host) {
		fail(w, http.StatusForbidden, "this is not lodge's address", gatewayHeaders)
		return
	}
	// 2. No Origin.
	if _, ok := r.Header["Origin"]; ok {
		fail(w, http.StatusForbidden, "browsers may not call the MCP gateway", gatewayHeaders)
		return
	}
	// 3. Method.
	switch r.Method {
	case http.MethodPost, http.MethodGet, http.MethodDelete:
	default:
		w.Header().Set("Allow", "POST, GET, DELETE")
		fail(w, http.StatusMethodNotAllowed, "method "+r.Method+" is not allowed here", gatewayHeaders)
		return
	}
	// 4. Peer: identity checks a and b. Tags and allow-lists are not
	// consulted.
	var d *denial
	if p, d = g.peerChecks(r, who); d != nil {
		fail(w, d.status, d.line, gatewayHeaders)
		return
	}
	// 5. Token, and 6. its scope. A live token without the scope is 403: the
	// client signs in again for more scope. Anything else is 401, with no
	// detail (RFC 9728).
	resource := g.cfg.OAuth.Issuer() + "/.well-known/oauth-protected-resource" + gatewayPath
	access, err := g.cfg.OAuth.Check(r.Context(), tokens.Bearer(r.Header.Get("Authorization")), oauth.ScopeMCP)
	switch {
	case errors.Is(err, oauth.ErrScope):
		w.Header().Set("WWW-Authenticate",
			`Bearer error="insufficient_scope", scope="`+oauth.ScopeMCP+`", resource_metadata="`+resource+`"`)
		fail(w, http.StatusForbidden, "the token has no scope "+oauth.ScopeMCP, gatewayHeaders)
		return
	case err != nil:
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+resource+`"`)
		fail(w, http.StatusUnauthorized, "unauthorized", gatewayHeaders)
		return
	}
	caller = mcpgw.Caller{Principal: access.Principal, Client: access.Client, Expires: access.Expires}
	// 7. No upgrade.
	if refuseUpgrade(w, r, gatewayHeaders) {
		return
	}
	gatewayHeaders(w.Header())
	g.cfg.MCP.ServeHTTP(w, r.WithContext(mcpgw.WithCaller(r.Context(), caller)))
}

// connectBody bounds an app call's body: 4 MiB
// (docs/specs/lodge-http.md, App calls, /app/<id>/connect/).
const connectBody = 4 << 20

// connectPatterns are the forms of a call's service and method.
var (
	connectService = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+$`)
	connectMethod  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	principalName  = regexp.MustCompile(`^[A-Za-z0-9@._-]{1,64}$`)
)

// connectCall serves /app/<id>/connect/<service>/<method>: 1 RPC of an app on
// hearth, called by its frame (docs/specs/lodge-http.md, App calls,
// /app/<id>/connect/). lodge writes nothing on it.
func (g *Gateway) connectCall(w http.ResponseWriter, r *http.Request, who peer.WhoIs, id string, a apps.App, installed bool) {
	flags := frameFlags{}
	if installed && a.Enabled() {
		flags = frameFlags{content: a.Manifest.Content, remoteImages: a.Manifest.RemoteImages}
	}
	headers := g.appHeaders(id, flags)
	// 1. Host.
	if !g.Names().lodgeHost(r.Host) {
		fail(w, http.StatusForbidden, "this is not lodge's address", headers)
		return
	}
	// 2. Origin: present, and lodge's own.
	origins := r.Header.Values("Origin")
	switch {
	case len(origins) == 0:
		fail(w, http.StatusForbidden, "an app call needs lodge's Origin", headers)
		return
	case len(origins) > 1 || origins[0] != g.Names().LodgeOrigin:
		fail(w, http.StatusForbidden, "requests from another origin are refused", headers)
		return
	}
	// 3. Fetch metadata, when present.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		fail(w, http.StatusForbidden, "requests from another site are refused", headers)
		return
	}
	// 4. Method, and no upgrade.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		fail(w, http.StatusMethodNotAllowed, "method "+r.Method+" is not allowed here", headers)
		return
	}
	if refuseUpgrade(w, r, headers) {
		return
	}
	// 5. Identity, every check.
	p, d := g.identity(r, who)
	if d != nil {
		fail(w, d.status, d.line, headers)
		return
	}
	// 6. App.
	switch {
	case !installed:
		fail(w, http.StatusNotFound, "no app is installed at /"+id, headers)
		return
	case !a.Enabled():
		fail(w, http.StatusServiceUnavailable, "app "+id+" is disabled: "+a.Reason, headers)
		return
	}
	// 7. Operation: 1 that the manifest exposes to the app's frame. An app
	// whose hearth manifest did not load is disabled, and never gets here.
	service, method, ok := connectTarget(r.URL.EscapedPath(), a.Hearth)
	if !ok {
		fail(w, http.StatusNotFound, "not found", headers)
		return
	}
	// 8. Content type, without its parameters.
	if ct := mediaType(r.Header.Get("Content-Type")); ct != "application/json" && ct != "application/proto" {
		fail(w, http.StatusUnsupportedMediaType, "an app call is application/json or application/proto", headers)
		return
	}
	// 9. Principal: a person's login, which an OAuth principal never is.
	if !principalLogin(p.Login) {
		fail(w, http.StatusForbidden, "login "+p.Login+" cannot be a principal of an app on hearth", headers)
		return
	}
	// A signed-in person may do everything in the app: every capability, on the
	// root path of every selector.
	items := make([]oauth.Item, 0, len(a.Hearth.Capabilities))
	for _, c := range a.Hearth.Capabilities {
		items = append(items, oauth.Item{Cap: c})
	}
	grant, err := mcpgw.GrantHeader(items)
	if err != nil {
		g.log.Error("make an app call's grant", "app", id, "err", err)
		fail(w, http.StatusInternalServerError, "lodge cannot make the grant", headers)
		return
	}
	if r.ContentLength > connectBody {
		// Read what the caller is still sending, up to 1 body's worth, so that
		// it reads the refusal instead of a reset connection.
		_, _ = io.Copy(io.Discard, &deadlineBody{ReadCloser: io.NopCloser(io.LimitReader(r.Body, connectBody)),
			rc: http.NewResponseController(w), timeout: g.cfg.BodyTimeout})
		fail(w, http.StatusRequestEntityTooLarge, "the call is larger than 4 MiB", headers)
		return
	}
	if body := r.Body; body != nil && body != http.NoBody {
		r.Body = &deadlineBody{ReadCloser: http.MaxBytesReader(w, body, connectBody),
			rc: http.NewResponseController(w), timeout: g.cfg.BodyTimeout}
	}
	g.proxy(w, r, proxyReq{app: a, kind: kindPrincipal, peer: p, principal: p.Login, grant: grant,
		escapedPath: "/connect/" + service + "/" + method, headers: headers, modify: headers})
}

// connectTarget reads the service and the method of an app call's path, and
// reports whether it names 1 operation that the manifest exposes to ui.
func connectTarget(escaped string, hearth *apps.Hearth) (service, method string, ok bool) {
	if hearth == nil {
		return "", "", false
	}
	segs, ok := segments(escaped)
	if !ok || len(segs) != 5 || segs[2] != "connect" {
		return "", "", false
	}
	service, method = segs[3], segs[4]
	if !connectService.MatchString(service) || !connectMethod.MatchString(method) {
		return "", "", false
	}
	name := service + "." + method
	if !slices.ContainsFunc(hearth.Operations, func(op apps.Operation) bool { return op.UI() && op.Name == name }) {
		return "", "", false
	}
	return service, method, true
}

// mediaType is a Content-Type without its parameters.
func mediaType(ct string) string {
	t, _, _ := strings.Cut(ct, ";")
	return strings.ToLower(strings.TrimSpace(t))
}

// principalLogin reports whether login may be an app call's principal: a
// person's login, which holds @, and never an OAuth principal's name.
func principalLogin(login string) bool {
	return principalName.MatchString(login) && strings.Contains(login, "@")
}
