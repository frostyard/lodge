//go:build e2e

package main

import (
	"net/http"

	"github.com/frostyard/lodge/internal/e2eid"
	"github.com/frostyard/lodge/internal/peer"
)

// e2eBuild is whether this is the development build: it joins no tailnet,
// and every loopback peer is e2eid's fixed identity.
const e2eBuild = true

func plainWhoIs() peer.WhoIs { return e2eid.WhoIs{} }

// plainContent sends Cross-Origin-Resource-Policy: cross-origin on content,
// since Chromium does not count two loopback ports as one site
// (docs/specs/lodge-http.md, The development build).
func plainContent(h http.Handler) http.Handler { return e2eid.ContentCORP(h) }
