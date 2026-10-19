package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/peer"
	"github.com/frostyard/lodge/internal/tokens"
	"github.com/frostyard/lodge/kit/lodgeapp"
)

// kind is a caller kind (docs/specs/app-contract.md, Caller headers).
type kind string

const (
	kindSession kind = "session"
	kindAgent   kind = "agent"
	kindDevice  kind = "device"
	kindContent kind = "content"
)

// proxyReq is one request to proxy to an app's socket.
type proxyReq struct {
	app  apps.App
	kind kind
	// peer is the caller's identity; for a device, named is whether WhoIs
	// named it at all.
	peer  peer.Peer
	named bool
	// escapedPath is the path on the app's socket, escaped, query apart.
	escapedPath string
	token       tokens.Token
	// session is the session's ID, on the /api upgrade only.
	session string
	// headers sets this kind's response headers, on errors too.
	headers func(http.Header)
	// modify rewrites the app's response headers.
	modify func(http.Header)
}

// stripClient deletes what a client must not pass to an app
// (docs/specs/lodge-http.md, Proxying): every Lodge-* and Tailscale-*
// header, forwarding headers, Cookie and, except for devices,
// Authorization and Origin. Names are compared with '_' read as '-', for
// servers that fold one into the other.
func stripClient(h http.Header, device bool) {
	for k := range h {
		n := strings.ReplaceAll(strings.ToLower(k), "_", "-")
		switch {
		case strings.HasPrefix(n, "lodge-"), strings.HasPrefix(n, "tailscale-"), strings.HasPrefix(n, "x-forwarded-"),
			n == "forwarded", n == "x-real-ip", n == "cookie":
			delete(h, k)
		case !device && (n == "authorization" || n == "origin"):
			delete(h, k)
		}
	}
}

// setCaller sets the caller headers for p's kind with the kit's
// lodgeapp.Caller, which deletes every Lodge-* header first, so lodge and
// the apps agree on the encoding.
func setCaller(h http.Header, p proxyReq) {
	c := lodgeapp.Caller{Kind: lodgeapp.Kind(p.kind)}
	switch p.kind {
	case kindSession, kindContent:
		c.Login, c.DisplayName, c.NodeID, c.NodeName = p.peer.Login, p.peer.DisplayName, p.peer.NodeID, nodeName(p.peer.NodeName)
		c.Session = p.session
	case kindAgent:
		c.Token, c.Scope = p.token.Name, p.token.Scope
	case kindDevice:
		if p.named {
			c.NodeID, c.NodeName = p.peer.NodeID, nodeName(p.peer.NodeName)
			if !p.peer.Tagged {
				c.Login = p.peer.Login
			}
		}
	}
	c.SetHeaders(h)
}

// nodeName keeps [a-z0-9-] of a node's short name.
func nodeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		}
		return -1
	}, s)
}

// proxy relays r to the app's socket as p says.
func (g *Gateway) proxy(w http.ResponseWriter, r *http.Request, p proxyReq) {
	unescaped, err := url.PathUnescape(p.escapedPath)
	if err != nil {
		fail(w, http.StatusNotFound, "not found", p.headers)
		return
	}
	rp := &httputil.ReverseProxy{
		Transport:     g.transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			stripClient(out.Header, p.kind == kindDevice)
			out.URL = &url.URL{Scheme: "http", Host: p.app.ID, Path: unescaped, RawPath: p.escapedPath, RawQuery: pr.In.URL.RawQuery}
			out.Host = p.app.ID
			setCaller(out.Header, p)
		},
		ModifyResponse: func(resp *http.Response) error {
			if p.modify != nil {
				p.modify(resp.Header)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			g.log.Warn("proxy to an app", "app", p.app.ID, "kind", p.kind, "err", err)
			fail(w, http.StatusBadGateway, "app "+p.app.ID+" cannot be reached", p.headers)
		},
	}
	rp.ServeHTTP(w, r)
}

// contentModify keeps the pass-through headers of an app's content
// response, and adds the sandbox set.
func contentModify(headers func(http.Header)) func(http.Header) {
	return func(h http.Header) {
		kept := http.Header{}
		for _, k := range contentPassThrough {
			if v := h.Values(k); len(v) > 0 {
				kept[k] = v
			}
		}
		for k := range h {
			delete(h, k)
		}
		for k, v := range kept {
			h[k] = v
		}
		if h.Get("Cache-Control") == "" {
			h.Set("Cache-Control", "private, no-cache")
		}
		headers(h)
	}
}
