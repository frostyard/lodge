package gateway

import (
	"net/http"
	"strings"

	"github.com/frostyard/lodge/internal/oauth"
	"github.com/frostyard/lodge/internal/peer"
)

// oauthTarget is the route an OAuth or consent path asks lodge for.
type oauthTarget struct {
	// route is the oauth package's route.
	route string
	// consent is whether the path is one of the consent or grants pages.
	consent bool
	// id is a consent request's ID.
	id string
}

// oauthPath sorts an /.well-known/oauth-… or /oauth/… path into its route,
// and reports false for any other path (docs/specs/lodge-http.md, OAuth
// routes).
func oauthPath(escaped string, segs []string) (oauthTarget, bool) {
	switch escaped {
	case "/.well-known/oauth-authorization-server":
		return oauthTarget{route: oauth.RouteMetadata}, true
	case "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/gateway/mcp":
		return oauthTarget{route: oauth.RouteResource}, true
	}
	if len(segs) < 2 || segs[0] != "oauth" {
		return oauthTarget{}, false
	}
	switch {
	case len(segs) == 2:
		switch segs[1] {
		case "register":
			return oauthTarget{route: oauth.RouteRegister}, true
		case "token":
			return oauthTarget{route: oauth.RouteToken}, true
		case "revoke":
			return oauthTarget{route: oauth.RouteRevoke}, true
		case "authorize":
			return oauthTarget{route: oauth.RouteAuthorize, consent: true}, true
		case "grants":
			return oauthTarget{route: oauth.RouteGrants, consent: true}, true
		}
	case len(segs) == 3 && segs[1] == "grants" && segs[2] == "revoke":
		return oauthTarget{route: oauth.RouteGrantRevoke, consent: true}, true
	case len(segs) == 3 && segs[1] == "consent" && segs[2] != "":
		return oauthTarget{route: oauth.RouteConsent, consent: true, id: segs[2]}, true
	}
	return oauthTarget{}, false
}

// oauthRoute serves an OAuth or consent path: its checks in order, and then
// the oauth package's answer.
func (g *Gateway) oauthRoute(w http.ResponseWriter, r *http.Request, who peer.WhoIs, escaped string, segs []string) {
	target, ok := oauthPath(escaped, segs)
	if !ok {
		fail(w, http.StatusNotFound, "not found", g.shellHeaders)
		return
	}
	if g.cfg.OAuth == nil {
		fail(w, http.StatusNotFound, "not found", g.consentHeaders)
		return
	}
	if target.consent {
		g.consentRoute(w, r, who, target)
		return
	}
	g.oauthRequest(w, r, who, target)
}

// oauthHeaders sets the headers of an OAuth response: the confined set of
// device responses, without Cross-Origin-Resource-Policy, and JSON.
func oauthHeaders(h http.Header) {
	apartHeaders(h)
	h.Del("Cross-Origin-Resource-Policy")
	h.Set("Cache-Control", "no-store")
	dropCredentialsAndCORS(h)
}

// oauthRequest runs the OAuth checks in order (docs/specs/lodge-http.md,
// OAuth routes): the Host, no Origin, the method, and the peer.
func (g *Gateway) oauthRequest(w http.ResponseWriter, r *http.Request, who peer.WhoIs, target oauthTarget) {
	// 1. Host.
	if !g.Names().lodgeHost(r.Host) {
		fail(w, http.StatusForbidden, "this is not lodge's address", oauthHeaders)
		return
	}
	// 2. No Origin: these routes are for MCP clients, not browsers.
	if _, ok := r.Header["Origin"]; ok {
		fail(w, http.StatusForbidden, "browsers may not call lodge's OAuth routes", oauthHeaders)
		return
	}
	// 3. Method.
	if allow := oauthAllow(target.route); !methodAllowed(r.Method, allow) {
		w.Header().Set("Allow", allow)
		fail(w, http.StatusMethodNotAllowed, "method "+r.Method+" is not allowed here", oauthHeaders)
		return
	}
	// 4. Peer: identity checks a and b. Tags and allow-lists are not
	// consulted: a client has no login.
	if _, d := g.peerChecks(r, who); d != nil {
		fail(w, d.status, d.line, oauthHeaders)
		return
	}
	oauthHeaders(w.Header())
	g.cfg.OAuth.ServeOAuth(w, r, target.route)
}

// oauthAllow is the methods an OAuth route takes.
func oauthAllow(route string) string {
	switch route {
	case oauth.RouteMetadata, oauth.RouteResource:
		return "GET, HEAD"
	}
	return http.MethodPost
}

// consentHeaders sets the headers of every consent response, errors
// included (docs/specs/lodge-http.md, OAuth routes).
func (g *Gateway) consentHeaders(h http.Header) {
	h.Set("Content-Security-Policy", oauth.PagePolicy(""))
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", permissionsPolicy)
	h.Set("Cache-Control", "no-store")
	dropCredentialsAndCORS(h)
}

// consentAllow is the methods a consent route takes.
func consentAllow(route string) string {
	switch route {
	case oauth.RouteConsent:
		return "GET, HEAD, POST"
	case oauth.RouteGrantRevoke:
		return http.MethodPost
	}
	return "GET, HEAD"
}

// consentRoute runs the consent checks in order, and then the page or the
// decision (docs/specs/lodge-http.md, OAuth routes). It is the one browser
// route that writes, under the checks, the CSRF token and the request hash
// (ADR-0023).
func (g *Gateway) consentRoute(w http.ResponseWriter, r *http.Request, who peer.WhoIs, target oauthTarget) {
	g.consentHeaders(w.Header())
	// 1. Host.
	if !g.Names().lodgeHost(r.Host) {
		fail(w, http.StatusForbidden, "this is not lodge's address", g.consentHeaders)
		return
	}
	post := r.Method == http.MethodPost
	// 2. Origin: lodge's own, which a GET may leave out.
	origins := r.Header.Values("Origin")
	switch {
	case len(origins) > 1:
		fail(w, http.StatusForbidden, "requests from another origin are refused", g.consentHeaders)
		return
	case len(origins) == 1 && origins[0] != g.Names().LodgeOrigin:
		fail(w, http.StatusForbidden, "requests from another origin are refused", g.consentHeaders)
		return
	case len(origins) == 0 && post:
		fail(w, http.StatusForbidden, "a decision needs lodge's Origin", g.consentHeaders)
		return
	}
	// 3. Fetch metadata.
	site, upgrade := r.Header.Get("Sec-Fetch-Site"), isWebSocket(r)
	navigation := !post && r.Method == http.MethodGet &&
		r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document"
	if site == "" {
		if post {
			fail(w, http.StatusForbidden, "a decision needs Sec-Fetch-Site", g.consentHeaders)
			return
		}
	} else if post {
		if site != "same-origin" {
			fail(w, http.StatusForbidden, "requests from another site are refused", g.consentHeaders)
			return
		}
	} else if site != "same-origin" && site != "none" && !navigation {
		fail(w, http.StatusForbidden, "requests from another site are refused", g.consentHeaders)
		return
	}
	if upgrade {
		fail(w, http.StatusBadRequest, "this route takes no upgrade", g.consentHeaders)
		return
	}
	// 4. Method.
	if allow := consentAllow(target.route); !methodAllowed(r.Method, allow) {
		w.Header().Set("Allow", allow)
		fail(w, http.StatusMethodNotAllowed, "method "+r.Method+" is not allowed here", g.consentHeaders)
		return
	}
	// 5. Identity, every check.
	p, d := g.identity(r, who)
	if d != nil {
		fail(w, d.status, d.line, g.consentHeaders)
		return
	}
	// 6. Admin.
	admin, err := g.cfg.Allow.AdminAllowed(r.Context(), p.Login)
	if err != nil {
		g.log.Error("read the admin list", "err", err)
		fail(w, http.StatusInternalServerError, "lodge cannot read the admin list", g.consentHeaders)
		return
	}
	if !admin {
		fail(w, http.StatusForbidden, "login "+p.Login+" is not an admin: lodgectl allow admin "+p.Login, g.consentHeaders)
		return
	}
	g.cfg.OAuth.ServeConsent(w, r, target.route, target.id, p.Login)
}

// methodAllowed reports whether method is one of allow, which a route names
// as for the Allow header.
func methodAllowed(method, allow string) bool {
	for _, m := range strings.Split(allow, ", ") {
		if method == m {
			return true
		}
	}
	return false
}
