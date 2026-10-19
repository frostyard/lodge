//go:build !e2e

package main

import "github.com/frostyard/lodge/internal/peer"

// e2eBuild is whether this is the development build.
const e2eBuild = false

// plainWhoIs names the peers of the plain loopback listeners: no one, in
// a release build, so browser and agent requests there are refused.
func plainWhoIs() peer.WhoIs { return peer.Nobody{} }
