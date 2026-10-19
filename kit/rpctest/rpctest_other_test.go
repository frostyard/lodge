//go:build !linux

package rpctest_test

import (
	"context"
	"net"
	"testing"

	"github.com/frostyard/lodge/kit/rpctest"
)

// Off Linux, Serve skips its test: jsonrpc.Listen serves no private socket.
func TestServeSkipsOffLinux(t *testing.T) {
	var skipped bool
	t.Run("serve", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		rpctest.Serve(t, func(context.Context, net.Listener) error { return nil })
		t.Error("Serve did not skip")
	})
	if !skipped {
		t.Error("Serve did not skip off Linux")
	}
}
