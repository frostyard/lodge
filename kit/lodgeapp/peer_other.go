//go:build !linux

package lodgeapp

import (
	"errors"
	"net"
)

// checkPeer refuses every connection: lodge and its apps run on Linux, and
// the peer check needs SO_PEERCRED.
func checkPeer(*net.UnixConn, int) error {
	return errors.New("lodge apps serve their socket on Linux only")
}
