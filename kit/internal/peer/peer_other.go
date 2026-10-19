//go:build !linux

package peer

import (
	"errors"
	"net"
)

// Check refuses every connection: the peer check needs SO_PEERCRED, which
// is Linux's.
func Check(*net.UnixConn, int) error {
	return errors.New("the peer check needs Linux")
}
