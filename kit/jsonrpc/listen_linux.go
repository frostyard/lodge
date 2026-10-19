package jsonrpc

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/frostyard/lodge/kit/internal/peer"
)

// Listen creates an app's private socket at path, mode 0600, in a
// directory that must be private (0700 and owned by the user; it is
// created if missing): the socket its CLI and desktop app use. A stale
// socket is replaced; a live one means the daemon is already running.
// Accept admits only processes of the same user (SO_PEERCRED), and closes
// the rest, logged to log when it is not nil. ListenWith names the daemon
// in its errors.
func Listen(path string, log *slog.Logger) (net.Listener, error) {
	return ListenWith(path, ListenOptions{Logger: log})
}

// ListenWith is Listen with options.
func ListenWith(path string, opts ListenOptions) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("socket directory: %w", err)
	}
	if err := checkPrivateDir(dir); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			if opts.Name != "" {
				return nil, fmt.Errorf("%s is already listening on %s", opts.Name, path)
			}
			return nil, fmt.Errorf("another server is already listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("listen: %w", err)
	}
	ln.SetUnlinkOnClose(true)
	return &private{UnixListener: ln, log: opts.Logger}, nil
}

func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.IsDir():
		return fmt.Errorf("socket directory %s is not a directory", dir)
	case !ok || int(st.Uid) != os.Getuid():
		return fmt.Errorf("socket directory %s is not owned by uid %d", dir, os.Getuid())
	case fi.Mode().Perm()&0o077 != 0:
		return fmt.Errorf("socket directory %s has mode %v; want 0700", dir, fi.Mode().Perm())
	}
	return nil
}

// private is a private socket: Accept admits only the user's processes.
type private struct {
	*net.UnixListener
	log *slog.Logger
}

func (l *private) Accept() (net.Conn, error) {
	for {
		nc, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		if err := peer.Check(nc, os.Getuid()); err != nil {
			if l.log != nil {
				l.log.Warn("rejected connection", "err", err)
			}
			_ = nc.Close()
			continue
		}
		return nc, nil
	}
}
