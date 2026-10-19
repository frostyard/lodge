//go:build e2e

package main

import (
	"github.com/frostyard/lodge/internal/e2eid"
	"github.com/frostyard/lodge/internal/peer"
)

// e2eBuild is whether this is the development build: it joins no tailnet,
// and every loopback peer is e2eid's fixed identity.
const e2eBuild = true

func plainWhoIs() peer.WhoIs { return e2eid.WhoIs{} }
