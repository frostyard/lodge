//go:build !linux

package lodgeapp

import (
	"context"
	"errors"
	"testing"
)

// Off Linux an app socket can only refuse every connection, so Listen and
// Serve say so at once instead.
func TestAppSocketsAreLinuxOnly(t *testing.T) {
	e := Env{Root: t.TempDir()}
	if ln, err := e.Listen("notes"); !errors.Is(err, ErrUnsupported) || ln != nil {
		t.Errorf("Listen = %v, %v; want ErrUnsupported", ln, err)
	}
	srv := &Server{Health: func(context.Context) Health { return Health{OK: true} }, API: nil}
	if err := srv.Serve(t.Context(), &Listener{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Serve = %v, want ErrUnsupported", err)
	}
}
