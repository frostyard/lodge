package peer

import (
	"errors"
	"net/netip"
	"testing"
)

func TestOwns(t *testing.T) {
	p := Peer{Addrs: []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("fd7a:115c:a1e0::1")}}
	for addr, want := range map[string]bool{
		"192.0.2.1":          true,
		"::ffff:192.0.2.1":   true,
		"fd7a:115c:a1e0::1":  true,
		"192.0.2.2":          false,
		"198.51.100.10":      false,
		"fd7a:115c:a1e0::99": false,
	} {
		if got := p.Owns(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Owns(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestSame(t *testing.T) {
	a := Peer{NodeID: "n1", Login: "alice@example.com", NodeName: "phone"}
	if !a.Same(Peer{NodeID: "n1", Login: "alice@example.com", NodeName: "renamed"}) {
		t.Error("a renamed node is not the same")
	}
	for _, b := range []Peer{
		{NodeID: "n2", Login: "alice@example.com"},
		{NodeID: "n1", Login: "bob@example.com"},
		{NodeID: "n1", Login: "alice@example.com", Tagged: true},
	} {
		if a.Same(b) {
			t.Errorf("%+v is the same as %+v", b, a)
		}
	}
}

func TestNobody(t *testing.T) {
	if _, err := (Nobody{}).WhoIs(t.Context(), "127.0.0.1:1234"); !errors.Is(err, ErrUnknown) {
		t.Errorf("Nobody.WhoIs = %v, want ErrUnknown", err)
	}
}
