package gateway

import (
	"net/http"
	"strings"
)

// Names are the addresses lodge answers to: LODGE and CONTENT, and their
// origins (docs/specs/lodge-http.md, Listeners and The development build).
type Names struct {
	// Lodge and Content are the Host values the two nodes accept.
	Lodge, Content string
	// LodgeOrigin, LodgeWS and ContentOrigin are the origins written into
	// the policies and compared with Origin.
	LodgeOrigin, LodgeWS, ContentOrigin string
	// tls accepts LODGE:443 and CONTENT:443 as well.
	tls bool
}

// TailnetNames are the names of the two tailnet nodes, by DNS name.
func TailnetNames(lodge, content string) Names {
	return Names{
		Lodge: lodge, Content: content,
		LodgeOrigin: "https://" + lodge, LodgeWS: "wss://" + lodge, ContentOrigin: "https://" + content,
		tls: true,
	}
}

// LoopbackNames are the development build's: plain HTTP on two loopback
// addresses ("127.0.0.1:PORT").
func LoopbackNames(lodge, content string) Names {
	return Names{
		Lodge: lodge, Content: content,
		LodgeOrigin: "http://" + lodge, LodgeWS: "ws://" + lodge, ContentOrigin: "http://" + content,
	}
}

func (n Names) lodgeHost(h string) bool {
	return h == n.Lodge || (n.tls && h == n.Lodge+":443")
}

func (n Names) contentHost(h string) bool {
	return h == n.Content || (n.tls && h == n.Content+":443")
}

// ShellPolicy is the shell's Content-Security-Policy.
func (n Names) ShellPolicy() string {
	l := n.LodgeOrigin + "/lodge/"
	return "default-src 'none'; script-src " + l + "; style-src " + l + " 'unsafe-inline'; img-src 'self' data:; " +
		"font-src " + l + " data:; connect-src " + l + "; manifest-src " + l + "; frame-src " + n.LodgeOrigin + "/app/; " +
		"worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}

// frameFlags are the manifest flags an app frame's policy depends on.
type frameFlags struct {
	content, remoteImages bool
}

// AppPolicy is app id's frame Content-Security-Policy.
func (n Names) AppPolicy(id string, content, remoteImages bool) string {
	base := n.LodgeOrigin + "/app/" + id + "/"
	img, media, frame := "'self' data:", "'self'", base
	if content {
		img += " " + n.ContentOrigin
		media += " " + n.ContentOrigin
		frame += " " + n.ContentOrigin
	}
	if remoteImages {
		img += " https:"
	}
	return "default-src 'none'; script-src " + base + "; style-src " + base + " 'unsafe-inline'; img-src " + img + "; " +
		"media-src " + media + "; font-src " + base + " data:; connect-src " + base + " " + n.LodgeWS + "/app/" + id + "/api; " +
		"frame-src " + frame + "; manifest-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; " +
		"form-action 'none'; frame-ancestors 'self'"
}

// ContentPolicy is the sandbox policy of every content response. links
// is the manifest's contentLinks (D6): a user's click on a link may open a
// new, unsandboxed window, and nothing else is allowed.
func (n Names) ContentPolicy(links bool) string {
	sandbox := "sandbox"
	if links {
		sandbox = "sandbox allow-popups allow-popups-to-escape-sandbox"
	}
	return "default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; " + sandbox +
		"; frame-ancestors " + n.LodgeOrigin
}

const permissionsPolicy = "camera=(), microphone=(), geolocation=()"

// browserHeaders sets the headers every browser response carries,
// replacing any of the same name, and keeping an app's own policy beside
// lodge's.
func browserHeaders(h http.Header, csp, frameOptions string) {
	h.Add("Content-Security-Policy", csp)
	h.Set("X-Frame-Options", frameOptions)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", permissionsPolicy)
	dropCredentialsAndCORS(h)
}

func (g *Gateway) shellHeaders(h http.Header) {
	browserHeaders(h, g.cfg.Names.ShellPolicy(), "DENY")
}

func (g *Gateway) appHeaders(id string, f frameFlags) func(http.Header) {
	return func(h http.Header) {
		browserHeaders(h, g.cfg.Names.AppPolicy(id, f.content, f.remoteImages), "SAMEORIGIN")
	}
}

func (g *Gateway) contentHeaders(links bool) func(http.Header) {
	return func(h http.Header) {
		h.Add("Content-Security-Policy", g.cfg.Names.ContentPolicy(links))
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-site")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		dropCredentialsAndCORS(h)
	}
}

func deviceHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Del("Set-Cookie")
}

func agentHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
}

// dropCredentialsAndCORS removes Set-Cookie and every Access-Control-*
// header: no browser or content response carries them.
func dropCredentialsAndCORS(h http.Header) {
	h.Del("Set-Cookie")
	for k := range h {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			delete(h, k)
		}
	}
}

// contentPassThrough are the app's response headers a content response
// keeps; lodge adds its own set after them.
var contentPassThrough = []string{
	"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Content-Disposition",
	"Cache-Control", "Etag", "Last-Modified", "Content-Security-Policy",
}
