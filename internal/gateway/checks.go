package gateway

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/frostyard/lodge/internal/peer"
)

// WhoIsCacheFor is how long a WhoIs answer is reused on one connection.
const WhoIsCacheFor = 60 * time.Second

// denial is a failed check: its status and its one line.
type denial struct {
	status int
	line   string
}

func deny(status int, line string) *denial { return &denial{status, line} }

// connKey carries a connection's WhoIs cache in its context.
type connKey struct{}

type connCache struct {
	mu   sync.Mutex
	peer peer.Peer
	at   time.Time
	ok   bool
}

// connContext gives each connection an empty WhoIs cache.
func connContext(ctx context.Context, _ net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, &connCache{})
}

// whoIs names the request's peer, reusing the connection's answer for up
// to WhoIsCacheFor. Failures are not cached.
func (g *Gateway) whoIs(r *http.Request, who peer.WhoIs) (peer.Peer, error) {
	c, _ := r.Context().Value(connKey{}).(*connCache)
	now := g.now()
	if c != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.ok && now.Sub(c.at) < WhoIsCacheFor {
			return c.peer, nil
		}
	}
	p, err := who.WhoIs(r.Context(), r.RemoteAddr)
	if err != nil {
		return peer.Peer{}, err
	}
	if c != nil {
		c.peer, c.at, c.ok = p, now, true
	}
	return p, nil
}

// remoteIP is the request's remote address without its port.
func remoteIP(remoteAddr string) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(remoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
}

// peerChecks are identity checks a and b: WhoIs names the peer, from one
// of the node's own addresses.
func (g *Gateway) peerChecks(r *http.Request, who peer.WhoIs) (peer.Peer, *denial) {
	p, err := g.whoIs(r, who)
	if err != nil {
		return peer.Peer{}, deny(http.StatusForbidden, "lodge cannot name this peer")
	}
	if ip, ok := remoteIP(r.RemoteAddr); !ok || !p.Owns(ip) {
		return peer.Peer{}, deny(http.StatusForbidden, "forwarded traffic is refused")
	}
	return p, nil
}

// identity runs every identity check, a to e (docs/specs/lodge-http.md,
// Identity). The allow-lists are read on every call.
func (g *Gateway) identity(r *http.Request, who peer.WhoIs) (peer.Peer, *denial) {
	p, d := g.peerChecks(r, who)
	if d != nil {
		return p, d
	}
	return p, g.allowed(r.Context(), p)
}

// allowed runs checks c, d and e on a named peer.
func (g *Gateway) allowed(ctx context.Context, p peer.Peer) *denial {
	if p.Tagged {
		return deny(http.StatusForbidden, "tagged nodes cannot sign in")
	}
	ok, err := g.cfg.Allow.LoginAllowed(ctx, p.Login)
	if err != nil {
		g.log.Error("read the allowed logins", "err", err)
		return deny(http.StatusInternalServerError, "lodge cannot read its allow-lists")
	}
	if !ok {
		return deny(http.StatusForbidden, "login "+p.Login+" is not allowed: lodgectl allow login "+p.Login)
	}
	ok, err = g.cfg.Allow.NodeAllowed(ctx, p.NodeID)
	if err != nil {
		g.log.Error("read the allowed nodes", "err", err)
		return deny(http.StatusInternalServerError, "lodge cannot read its allow-lists")
	}
	if !ok {
		return deny(http.StatusForbidden, "node "+p.NodeName+" ("+p.NodeID+") is not allowed: lodgectl allow node "+p.NodeID)
	}
	return nil
}

// recheck asks WhoIs again, without the cache, for a live session's
// remote address, and re-runs the identity checks.
func (g *Gateway) recheck(ctx context.Context, who peer.WhoIs, remoteAddr string, was peer.Peer) *denial {
	p, err := who.WhoIs(ctx, remoteAddr)
	if err != nil {
		return deny(http.StatusForbidden, "lodge cannot name this peer")
	}
	if !p.Same(was) {
		return deny(http.StatusForbidden, "the peer at this address has changed")
	}
	if ip, ok := remoteIP(remoteAddr); !ok || !p.Owns(ip) {
		return deny(http.StatusForbidden, "forwarded traffic is refused")
	}
	return g.allowed(ctx, p)
}

// browserRoute says which browser route a request is on.
type browserRoute struct {
	// shellPage admits a top-level navigation from any site.
	shellPage bool
	// api is /app/<id>/api: GET only, and an upgrade's stricter checks.
	api bool
}

// browserChecks runs the browser checks in order (docs/specs/lodge-http.md,
// Browser checks).
func (g *Gateway) browserChecks(r *http.Request, who peer.WhoIs, rt browserRoute) (peer.Peer, *denial) {
	// 1. Host.
	if !g.cfg.Names.lodgeHost(r.Host) {
		return peer.Peer{}, deny(http.StatusForbidden, "this is not lodge's address")
	}
	upgrade := isWebSocket(r)
	// 2. Origin: exact when present, and required on an upgrade.
	origins := r.Header.Values("Origin")
	switch {
	case len(origins) > 1 || (len(origins) == 1 && origins[0] != g.cfg.Names.LodgeOrigin):
		return peer.Peer{}, deny(http.StatusForbidden, "requests from another origin are refused")
	case len(origins) == 0 && upgrade:
		return peer.Peer{}, deny(http.StatusForbidden, "a WebSocket needs lodge's Origin")
	}
	// 3. Fetch metadata, when present.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		navigation := rt.shellPage && r.Method == http.MethodGet &&
			r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document"
		ok := site == "same-origin" || (site == "none" && !upgrade)
		if !ok && !navigation {
			return peer.Peer{}, deny(http.StatusForbidden, "requests from another site are refused")
		}
	}
	// 4. Method.
	if r.Method != http.MethodGet && (rt.api || r.Method != http.MethodHead) {
		return peer.Peer{}, deny(http.StatusMethodNotAllowed, "method "+r.Method+" is not allowed here")
	}
	// 5. Identity.
	return g.identity(r, who)
}

// isWebSocket reports whether r asks to upgrade to a WebSocket.
func isWebSocket(r *http.Request) bool {
	return headerHasToken(r.Header, "Connection", "upgrade") && headerHasToken(r.Header, "Upgrade", "websocket")
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for part := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}
