//go:build !linux

package kittest_test

import (
	"testing"

	"github.com/frostyard/lodge/kit/kittest"
	"github.com/frostyard/lodge/kit/lodgeapp"
)

// Off Linux, Start and Attach skip their test rather than serve a socket
// whose every connection the peer check would drop.
func TestStartAndAttachSkipOffLinux(t *testing.T) {
	for name, start := range map[string]func(t *testing.T){
		"Start":  func(t *testing.T) { kittest.Start(t, "notes", &lodgeapp.Server{}) },
		"Attach": func(t *testing.T) { kittest.Attach(t, "notes", t.TempDir()) },
	} {
		var skipped bool
		t.Run(name, func(t *testing.T) {
			defer func() { skipped = t.Skipped() }()
			start(t)
			t.Errorf("%s did not skip", name)
		})
		if !skipped {
			t.Errorf("%s did not skip off Linux", name)
		}
	}
}
