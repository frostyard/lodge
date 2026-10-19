//go:build e2e

package e2eid

import (
	"errors"
	"testing"

	"github.com/frostyard/lodge/internal/peer"
)

func TestWhoIs(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:5000", "127.9.9.9:1", "[::1]:5000"} {
		p, err := WhoIs{}.WhoIs(t.Context(), addr)
		if err != nil || p.Login != Login || p.NodeID != NodeID || p.NodeName != NodeName || p.Tagged || len(p.Addrs) != 1 {
			t.Errorf("%s: %+v %v", addr, p, err)
		}
	}
	for _, addr := range []string{"192.0.2.1:5000", "[2001:db8::1]:1", "junk"} {
		if _, err := (WhoIs{}).WhoIs(t.Context(), addr); !errors.Is(err, peer.ErrUnknown) {
			t.Errorf("%s: %v, want ErrUnknown", addr, err)
		}
	}
}
