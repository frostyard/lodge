//go:build !linux

package lodgeapp

// supported reports whether this system can serve an app socket: the peer
// check needs SO_PEERCRED, which is Linux's.
const supported = false
