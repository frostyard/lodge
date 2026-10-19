//go:build e2e

// Package e2eid is the development build's one fixed identity
// (docs/specs/lodge-http.md, The development build). Every peer on its
// loopback listeners is the same untagged node, which still passes through
// every check after WhoIs, the allow-lists included. Every file of this
// package carries the e2e build tag, and a test proves with go tool nm that
// a release build holds none of it.
package e2eid

import (
	"context"
	"net/netip"

	"github.com/frostyard/lodge/internal/peer"
)

// The identity.
const (
	Login       = "e2e@example.com"
	DisplayName = "E2E"
	NodeName    = "e2e"
	NodeID      = "e2e-node"
)

// WhoIs names every loopback peer as the fixed identity, from its own
// address, and no one else.
type WhoIs struct{}

// WhoIs returns the fixed identity for a loopback remote address.
func (WhoIs) WhoIs(_ context.Context, remoteAddr string) (peer.Peer, error) {
	ap, err := netip.ParseAddrPort(remoteAddr)
	if err != nil || !ap.Addr().Unmap().IsLoopback() {
		return peer.Peer{}, peer.ErrUnknown
	}
	return peer.Peer{
		NodeID:      NodeID,
		NodeName:    NodeName,
		Addrs:       []netip.Addr{ap.Addr().Unmap()},
		Login:       Login,
		DisplayName: DisplayName,
	}, nil
}
