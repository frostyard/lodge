package kittest_test

import (
	"testing"

	"github.com/frostyard/lodge/kit/kittest"
	"github.com/frostyard/lodge/kit/lodgeapp"
)

// On Linux, Attach gives lodge's side of a socket under the root, without
// skipping.
func TestAttachOnLinux(t *testing.T) {
	root := t.TempDir()
	l := kittest.Attach(t, "notes", root)
	if want := (lodgeapp.Env{Root: root}).SocketPath("notes"); l.Socket != want || l.ID != "notes" {
		t.Errorf("Attach = %q %q, want %q", l.ID, l.Socket, want)
	}
}
