//go:build linux

package lodgeapp

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// shortRoot is a temp root short enough for socket paths (108 bytes).
func shortRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lodge")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestLoadEnv(t *testing.T) {
	t.Setenv("LODGE_ROOT", "/srv/dev")
	t.Setenv("LODGE_UID", "")
	e, err := LoadEnv()
	if err != nil || e != (Env{Root: "/srv/dev", LodgeUID: os.Getuid()}) {
		t.Errorf("LoadEnv = %+v, %v; want the root and our own uid", e, err)
	}
	if got := e.SocketPath("notes"); got != "/srv/dev/run/lodge/apps/notes.sock" {
		t.Errorf("SocketPath = %q", got)
	}
	if got := e.Path(InferenceSocket); got != "/srv/dev/run/lodge/inference.sock" {
		t.Errorf("Path = %q", got)
	}
	t.Setenv("LODGE_UID", "4242")
	if e, err := LoadEnv(); err != nil || e.LodgeUID != 4242 {
		t.Errorf("LoadEnv with LODGE_UID = %+v, %v", e, err)
	}
	for _, bad := range []string{"x", "-1"} {
		t.Setenv("LODGE_UID", bad)
		if _, err := LoadEnv(); err == nil {
			t.Errorf("LODGE_UID=%q: no error", bad)
		}
	}
	t.Setenv("LODGE_UID", "")
	t.Setenv("LODGE_ROOT", "relative")
	if _, err := LoadEnv(); err == nil {
		t.Error("a relative LODGE_ROOT: no error")
	}
	if got := (Env{}).SocketPath("notes"); got != "/run/lodge/apps/notes.sock" {
		t.Errorf("production SocketPath = %q", got)
	}
}

func TestListenCreatesTheSocket(t *testing.T) {
	e := Env{Root: shortRoot(t), LodgeUID: os.Getuid()}
	ln, err := e.Listen("notes")
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(e.SocketPath("notes"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o660 {
		t.Errorf("socket mode %v, want a 0660 socket", fi.Mode())
	}
	if _, err := e.Listen("notes"); err == nil || !strings.Contains(err.Error(), "listening") {
		t.Errorf("a second Listen = %v, want it refused", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(e.SocketPath("notes")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Close left the socket: %v", err)
	}
}

func TestListenReplacesAStaleSocket(t *testing.T) {
	e := Env{Root: shortRoot(t), LodgeUID: os.Getuid()}
	path := e.SocketPath("notes")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	ln, err := e.Listen("notes")
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	_ = ln.Close()
}

func TestListenRefusesBadIDs(t *testing.T) {
	e := Env{Root: shortRoot(t), LodgeUID: os.Getuid()}
	for _, id := range []string{"", "Notes", "../x", "lodge", "mcp", "a234567890123456x"} {
		if _, err := e.Listen(id); err == nil {
			t.Errorf("Listen(%q): no error", id)
		}
	}
}

func TestAcceptAdmitsOnlyLodge(t *testing.T) {
	root := shortRoot(t)
	for _, tc := range []struct {
		uid   int
		admit bool
	}{{os.Getuid(), true}, {os.Getuid() + 1, false}} {
		e := Env{Root: filepath.Join(root, strconv.Itoa(tc.uid)), LodgeUID: tc.uid}
		ln, err := e.Listen("notes")
		if err != nil {
			t.Fatal(err)
		}
		accepted := make(chan net.Conn, 1)
		go func() {
			defer close(accepted)
			if nc, err := ln.Accept(); err == nil {
				accepted <- nc
			}
		}()
		nc, err := net.Dial("unix", e.SocketPath("notes"))
		if err != nil {
			t.Fatal(err)
		}
		if tc.admit {
			if c, ok := <-accepted; !ok {
				t.Error("lodge's uid was refused")
			} else {
				_ = c.Close()
			}
		} else if n, err := nc.Read(make([]byte, 1)); n != 0 || err == nil {
			t.Errorf("another uid read %d bytes, %v; want the connection closed", n, err)
		}
		_ = nc.Close()
		_ = ln.Close()
		<-accepted
	}
}

func TestPassedSocketMustBeUnix(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	f, err := tcp.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileListener(f); err == nil || !strings.Contains(err.Error(), "not a Unix socket") {
		t.Errorf("a passed TCP socket = %v, want it refused", err)
	}

	path := filepath.Join(shortRoot(t), "s.sock")
	ul, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ul.Close()
	f, err = ul.File()
	if err != nil {
		t.Fatal(err)
	}
	got, err := fileListener(f)
	if err != nil {
		t.Fatalf("a passed Unix socket: %v", err)
	}
	_ = got.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("closing a passed socket removed its path: %v", err)
	}
}

func TestActivatedNeedsOurPID(t *testing.T) {
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()+1))
	t.Setenv("LISTEN_FDS", "1")
	if ln, err := activated("notes"); ln != nil || err != nil {
		t.Errorf("another process's LISTEN_FDS = %v, %v; want ignored", ln, err)
	}
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "2")
	t.Setenv("LISTEN_FDNAMES", "mail:other")
	if _, err := activated("notes"); err == nil || !strings.Contains(err.Error(), "none is named") {
		t.Errorf("two passed sockets, none ours = %v", err)
	}
}
