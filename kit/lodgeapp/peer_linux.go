package lodgeapp

import (
	"fmt"
	"net"
	"syscall"
)

// checkPeer admits a connection only from uid (SO_PEERCRED).
func checkPeer(nc *net.UnixConn, uid int) error {
	raw, err := nc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return fmt.Errorf("peer credentials: %w", credErr)
	}
	if int(cred.Uid) != uid {
		return fmt.Errorf("peer uid %d is not lodge's (%d)", cred.Uid, uid)
	}
	return nil
}
