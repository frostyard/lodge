//go:build linux

package jsonrpc_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frostyard/lodge/kit/jsonrpc"
	"github.com/frostyard/lodge/kit/rpctest"
)

func TestListenMakesAPrivateSocket(t *testing.T) {
	path := rpctest.Socket(t)
	ln, err := jsonrpc.Listen(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("socket %v, %v; want mode 0600", fi.Mode(), err)
	}
	if _, err := jsonrpc.Listen(path, nil); err == nil || err.Error() != "another server is already listening on "+path {
		t.Errorf("Listen on a live socket = %v", err)
	}
	if _, err := jsonrpc.ListenWith(path, jsonrpc.ListenOptions{Name: "notesd"}); err == nil ||
		err.Error() != "notesd is already listening on "+path {
		t.Errorf("ListenWith a name on a live socket = %v, want the daemon named", err)
	}
	accepted := make(chan error, 1)
	go func() {
		nc, err := ln.Accept()
		if err == nil {
			_ = nc.Close()
		}
		accepted <- err
	}()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if err := <-accepted; err != nil {
		t.Errorf("our own uid was refused: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	// A socket left behind by a crash is replaced.
	stale, err := jsonrpc.Listen(path, nil)
	if err != nil {
		t.Fatalf("Listen after Close: %v", err)
	}
	_ = stale.Close()
}

func TestListenRefusesAnOpenDirectory(t *testing.T) {
	dir := filepath.Dir(rpctest.Socket(t))
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := jsonrpc.Listen(filepath.Join(dir, "s.sock"), nil); err == nil || !strings.Contains(err.Error(), "want 0700") {
		t.Errorf("Listen in a 0755 directory = %v", err)
	}
}
