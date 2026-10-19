// Package peer is who is on the other end of a connection, as lodge's own
// tailnet node names it (docs/specs/lodge-http.md, Identity). It holds the
// type and the interface only, so the gateway and its tests never import
// tailscale.com: internal/tailnet implements WhoIs for the two nodes, and
// tests and the development build give their own.
package peer

import (
	"context"
	"errors"
	"net/netip"
	"slices"
)

// ErrUnknown means WhoIs names no peer for the address.
var ErrUnknown = errors.New("peer: unknown")

// Peer is WhoIs's answer for one remote address.
type Peer struct {
	// NodeID is the node's Tailscale StableNodeID.
	NodeID string
	// NodeName is the first label of the node's DNS name.
	NodeName string
	// Addrs are the node's own Tailscale addresses, never routes it carries.
	Addrs []netip.Addr
	// Tagged is whether the node is tagged, and so belongs to no user.
	Tagged bool
	// Login and DisplayName are the node owner's.
	Login       string
	DisplayName string
}

// Owns reports whether addr is one of the node's own addresses (check b).
func (p Peer) Owns(addr netip.Addr) bool {
	return slices.Contains(p.Addrs, addr.Unmap())
}

// Same reports whether p and q are the same node and owner, for a
// session's re-check.
func (p Peer) Same(q Peer) bool {
	return p.NodeID == q.NodeID && p.Login == q.Login && p.Tagged == q.Tagged
}

// WhoIs names the peer at a remote address ("ip:port") of one listener.
// It returns ErrUnknown, or another error, when it cannot.
type WhoIs interface {
	WhoIs(ctx context.Context, remoteAddr string) (Peer, error)
}

// Nobody names no one. A plain loopback listener of a release build uses
// it, so browser and agent requests there fail check a.
type Nobody struct{}

// WhoIs always returns ErrUnknown.
func (Nobody) WhoIs(context.Context, string) (Peer, error) { return Peer{}, ErrUnknown }
