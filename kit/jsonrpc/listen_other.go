//go:build !linux

package jsonrpc

import (
	"errors"
	"log/slog"
	"net"
)

// Listen serves a private Unix socket on Linux only. On Windows an app
// keeps its own named pipe, whose listener checks the peer itself.
func Listen(path string, log *slog.Logger) (net.Listener, error) {
	return ListenWith(path, ListenOptions{Logger: log})
}

// ListenWith is Listen with options.
func ListenWith(string, ListenOptions) (net.Listener, error) {
	return nil, errors.New("jsonrpc: Listen serves a private Unix socket on Linux only")
}
