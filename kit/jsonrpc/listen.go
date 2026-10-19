package jsonrpc

import "log/slog"

// ListenOptions configures ListenWith.
type ListenOptions struct {
	// Name is the daemon's, such as "notesd": "notesd is already listening
	// on PATH". Empty says "another server".
	Name string
	// Logger receives refused connections. Nil discards them.
	Logger *slog.Logger
}
