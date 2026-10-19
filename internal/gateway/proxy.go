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

// setCaller sets the caller headers for p's kind.
func setCaller(h http.Header, p proxyReq) {
	h.Set("Lodge-Caller", string(p.kind))
	switch p.kind {
	case kindSession, kindContent:
		setIdentity(h, p.peer)
	case kindAgent:
		h.Set("Lodge-Token", p.token.Name)
		h.Set("Lodge-Scope", p.token.Scope)
	case kindDevice:
		if p.named {
			h.Set("Lodge-Node-ID", p.peer.NodeID)
			h.Set("Lodge-Node-Name", nodeName(p.peer.NodeName))
			if !p.peer.Tagged {
				h.Set("Lodge-Login", PercentEncode(p.peer.Login))
			}
		}
	}
}

// setIdentity sets the four identity headers of session and content
// callers, Lodge-Display-Name only when it is not empty.
func setIdentity(h http.Header, p peer.Peer) {
	h.Set("Lodge-Login", PercentEncode(p.Login))
	if p.DisplayName != "" {
		h.Set("Lodge-Display-Name", PercentEncode(p.DisplayName))
	}
	h.Set("Lodge-Node-ID", p.NodeID)
	h.Set("Lodge-Node-Name", nodeName(p.NodeName))
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

// PercentEncode encodes every byte of s outside A-Z a-z 0-9 - . _ ~ @ +
// (docs/specs/app-contract.md, Caller headers).
func PercentEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~', c == '@', c == '+':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
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
