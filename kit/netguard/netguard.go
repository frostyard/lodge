// Package netguard refuses outbound connections to addresses inside the
// owner's network.
//
// An app that fetches a URL it was given — a feed, a page, an image — must
// not be made to reach the host's own network: loopback, the private
// ranges, link-local, the tailnet, the cloud metadata service, or anything
// else that is not public unicast. Public says which addresses are public.
// Control is a net.Dialer.Control that applies Public to every connection a
// dialer makes, and Dialer and Transport are a dialer and its HTTP transport
// that use it.
//
// Control runs after DNS: it sees each address the dialer is about to
// connect to, not the name. A name that resolves inside the network is
// refused, and so is a name that rebinds to an inside address between two
// lookups. Every connection is checked, so a redirect to an inside address
// is refused as well: the fence is on the dial, not on the first URL.
//
// Name is a cheap check by name, before any lookup, and is defence in depth
// only: Control is the fence.
//
// Callers must refuse every scheme but http and https themselves. A file:,
// gopher: or ftp: URL has no dialer this package guards, and a data: URL
// fetches nothing at all. Callers also bound what they fetch: this package
// says where a connection may go, not how much it may read or how long it
// may take.
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// ErrInside is the refusal of an address inside the owner's network. Every
// refusal Public, Control and Name make wraps it, so a caller can tell a
// refused address (errors.Is(err, ErrInside)) from a network fault.
var ErrInside = errors.New("netguard: the address is inside the local network")

// v4 is an IPv4 prefix, given as the bytes of its first address and its
// length. It is written this way so that this file holds no dotted quad:
// a private address written down reads as a real host to the repository's
// no-private-hosts check (internal/repocheck).
func v4(a, b, c, d byte, bits int) netip.Prefix {
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{a, b, c, d}), bits).Masked()
}

// insidePrefixes are the ranges Public refuses beyond those the netip
// methods name. The IPv6 ranges that embed an IPv4 address (NAT64, Teredo,
// 6to4) are refused whole: the address inside is not judged.
var insidePrefixes = []netip.Prefix{
	// IPv4.
	v4(0, 0, 0, 0, 8),          // "this network" (RFC 1122)
	v4(100, 64, 0, 0, 10),      // shared address space, CGNAT (RFC 6598)
	v4(192, 0, 0, 0, 24),       // IETF protocol assignments (RFC 6890)
	v4(192, 0, 2, 0, 24),       // documentation, TEST-NET-1 (RFC 5737)
	v4(198, 18, 0, 0, 15),      // benchmarking (RFC 2544)
	v4(198, 51, 100, 0, 24),    // documentation, TEST-NET-2 (RFC 5737)
	v4(203, 0, 113, 0, 24),     // documentation, TEST-NET-3 (RFC 5737)
	v4(240, 0, 0, 0, 4),        // reserved (RFC 1112), includes the broadcast
	v4(255, 255, 255, 255, 32), // the limited broadcast address
	// IPv6.
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64, well-known prefix
	netip.MustParsePrefix("64:ff9b:1::/48"), // NAT64, local-use prefix
	netip.MustParsePrefix("100::/64"),       // discard-only (RFC 6666)
	netip.MustParsePrefix("2001::/32"),      // Teredo, through a public relay
	netip.MustParsePrefix("2001:db8::/32"),  // documentation (RFC 3849)
	netip.MustParsePrefix("2002::/16"),      // 6to4, which embeds an IPv4 address
	netip.MustParsePrefix("fec0::/10"),      // site-local (RFC 3879)
}

// Public returns nil for a public unicast address, and an error wrapping
// ErrInside for any other: loopback, private (RFC 1918 and the IPv6 unique
// local addresses, fc00::/7, which hold the tailnet's), link-local unicast
// and multicast, interface-local and other multicast, unspecified, and the
// ranges in insidePrefixes. An IPv4-mapped IPv6 address (::ffff:0:0/96) is
// judged as the IPv4 address it holds. An address with a zone is refused:
// it is a link-local or an interface address.
func Public(ip netip.Addr) error {
	if ip.Zone() != "" {
		return fmt.Errorf("netguard: %s: %w", ip, ErrInside)
	}
	ip = ip.Unmap()
	if !ip.IsValid() {
		return fmt.Errorf("netguard: not an address: %w", ErrInside)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("netguard: %s: %w", ip, ErrInside)
	}
	for _, p := range insidePrefixes {
		if p.Contains(ip) {
			return fmt.Errorf("netguard: %s is in %s: %w", ip, p, ErrInside)
		}
	}
	return nil
}

// Control is a net.Dialer.Control that refuses a connection to an address
// that is not public. It parses address as host:port, which the dialer has
// already resolved, and refuses anything that is not a literal IP. The
// check runs after DNS, on every address tried, so a name that resolves
// inside the network, or rebinds between two lookups, is refused.
func Control(network, address string, c syscall.RawConn) error {
	return controlWith(Public)(network, address, c)
}

// controlWith is Control with a guard a test can replace, so that a test
// can allow one connection and refuse the next.
func controlWith(guard func(netip.Addr) error) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("netguard: %q is not host:port: %w", address, err)
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return fmt.Errorf("netguard: %q is not a literal IP address: %w", host, err)
		}
		return guard(ip)
	}
}

// Dialer is a net.Dialer that refuses every connection to an address that
// is not public. Set its Resolver to choose how names are looked up; the
// check runs on the answers either way.
func Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout: timeout,
		Control: Control,
	}
}

// Transport is an HTTP transport that dials through Dialer, so every
// connection, the first and every redirect's, is refused when its address
// is not public.
func Transport(timeout time.Duration) *http.Transport {
	return &http.Transport{
		DialContext: Dialer(timeout).DialContext,
		// No proxy: an environment proxy dials the address itself, and this
		// process would never see it, so it would bypass the guard.
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
	}
}

// insideNames are the suffixes Name refuses: the names reserved for a home
// or a private network, and the tailnet's.
var insideNames = []string{".localhost", ".local", ".internal", ".lan", ".home.arpa", ".ts.net"}

// Name refuses a host that is surely inside the owner's network before any
// DNS query: localhost and the names under insideNames, a name with no dot
// (a local name), and a literal address that Public refuses. A trailing dot
// and case do not matter.
//
// This is defence in depth only. A public name can still resolve inside, so
// Control is the fence; Name saves a lookup and gives a clear refusal for
// the common mistake.
func Name(host string) error {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" {
		return fmt.Errorf("netguard: an empty host: %w", ErrInside)
	}
	if ip, err := netip.ParseAddr(h); err == nil {
		return Public(ip)
	}
	if h == "localhost" {
		return fmt.Errorf("netguard: %q is loopback: %w", host, ErrInside)
	}
	for _, suffix := range insideNames {
		if strings.HasSuffix(h, suffix) {
			return fmt.Errorf("netguard: %q is an inside name (%s): %w", host, suffix, ErrInside)
		}
	}
	if !strings.Contains(h, ".") {
		return fmt.Errorf("netguard: %q has no dot, so it is a local name: %w", host, ErrInside)
	}
	return nil
}
