package lodgeapp

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/frostyard/lodge/kit/internal/peer"
	"github.com/frostyard/lodge/kit/manifest"
)

// socketMode is an app socket's mode: the app's user and group lodge.
const socketMode = 0o660

// Listener is an app socket. Accept admits only connections whose peer uid
// (SO_PEERCRED) is lodge's, and closes the rest before reading anything
// from them (Rule 2).
type Listener struct {
	ln  *net.UnixListener
	uid int
}

// Listen returns app id's socket: the one systemd passed (socket
// activation, LISTEN_FDS), or else a new one at e.SocketPath(id).
//
// A socket it creates gets the install layout's mode, 0660, and in
// production group lodge, before anything can be accepted. An app's user
// is not in group lodge, so only root can do that; a production daemon
// should take its socket from a systemd socket unit instead
// (SocketUser=<user>, SocketGroup=lodge, SocketMode=0660). Under e.Root
// the socket keeps the developer's own group.
func (e Env) Listen(id string) (*Listener, error) {
	if !manifest.ValidID(id) {
		return nil, fmt.Errorf("app id %q is not valid", id)
	}
	if ln, err := activated(id); ln != nil || err != nil {
		if err != nil {
			return nil, err
		}
		return &Listener{ln: ln, uid: e.LodgeUID}, nil
	}
	ln, err := e.create(e.SocketPath(id))
	if err != nil {
		return nil, err
	}
	return &Listener{ln: ln, uid: e.LodgeUID}, nil
}

func (e Env) create(path string) (*net.UnixListener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("socket directory: %w", err)
	}
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("another process is listening on %s", path)
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
	ln.SetUnlinkOnClose(true)
	if err := e.own(path); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// own gives a new socket its owners and mode. Until then, the peer check
// alone keeps other users out.
func (e Env) own(path string) error {
	if e.Root == "" {
		g, err := user.LookupGroup(LodgeGroup)
		if err != nil {
			return fmt.Errorf("lodge's group: %w", err)
		}
		gid, err := strconv.Atoi(g.Gid)
		if err != nil {
			return fmt.Errorf("lodge's gid %q: %w", g.Gid, err)
		}
		if err := os.Chown(path, -1, gid); err != nil {
			return fmt.Errorf("give the socket group %s (use a systemd socket unit): %w", LodgeGroup, err)
		}
	}
	if err := os.Chmod(path, socketMode); err != nil {
		return fmt.Errorf("socket mode: %w", err)
	}
	return nil
}

// activated returns the socket systemd passed for app id, or nil when
// systemd passed none (sd_listen_fds(3)). With several, it takes the one
// whose FileDescriptorName is id.
func activated(id string) (*net.UnixListener, error) {
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) {
		return nil, nil
	}
	n, err := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if err != nil || n < 1 {
		return nil, nil
	}
	const first = 3 // SD_LISTEN_FDS_START
	fd := first
	if n > 1 {
		names := strings.Split(os.Getenv("LISTEN_FDNAMES"), ":")
		i := -1
		for j, name := range names {
			if name == id {
				i = j
			}
		}
		if i < 0 || i >= n {
			return nil, fmt.Errorf("systemd passed %d sockets, and none is named %q (FileDescriptorName=)", n, id)
		}
		fd = first + i
	}
	return fileListener(os.NewFile(uintptr(fd), id))
}

// fileListener returns the Unix stream socket in f, and closes f. A socket
// on the network is refused (Rule 1).
func fileListener(f *os.File) (*net.UnixListener, error) {
	defer func() { _ = f.Close() }()
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("passed socket: %w", err)
	}
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		_ = ln.Close()
		return nil, fmt.Errorf("passed socket is %s, not a Unix socket", ln.Addr().Network())
	}
	ul.SetUnlinkOnClose(false) // systemd owns the path
	return ul, nil
}

// Accept waits for the next connection from lodge's uid.
func (l *Listener) Accept() (net.Conn, error) { return l.accept(nil) }

func (l *Listener) accept(log *slog.Logger) (net.Conn, error) {
	for {
		nc, err := l.ln.AcceptUnix()
		if err != nil {
			return nil, err
		}
		if err := peer.Check(nc, l.uid); err != nil {
			if log != nil {
				log.Warn("refused a connection", "err", err)
			}
			_ = nc.Close()
			continue
		}
		return nc, nil
	}
}

// Close stops listening, and removes the socket if Listen created it.
func (l *Listener) Close() error { return l.ln.Close() }

// Addr returns the socket's address.
func (l *Listener) Addr() net.Addr { return l.ln.Addr() }

var _ net.Listener = (*Listener)(nil)

// logged is a Listener whose refusals are logged, for Server.Serve.
type logged struct {
	*Listener
	log *slog.Logger
}

func (l logged) Accept() (net.Conn, error) { return l.accept(l.log) }
