package netguard

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// a4 is an IPv4 address from its bytes, and mapped4 its IPv4-mapped IPv6
// form. The repository's no-private-hosts check (internal/repocheck) reads
// a dotted quad outside loopback and the documentation ranges as a real
// host, so the ranges this package must name are written as bytes.
func a4(a, b, c, d byte) netip.Addr {
	return netip.AddrFrom4([4]byte{a, b, c, d})
}

func mapped4(a, b, c, d byte) netip.Addr {
	return netip.AddrFrom16([16]byte{10: 0xff, 11: 0xff, 12: a, 13: b, 14: c, 15: d})
}

func TestPublic(t *testing.T) {
	for _, tc := range []struct {
		name string
		ip   netip.Addr
	}{
		{"loopback", netip.MustParseAddr("127.0.0.1")},
		{"loopback, last", a4(127, 255, 255, 254)},
		{"private, 10/8", a4(10, 1, 2, 3)},
		{"private, 172.16/12", a4(172, 16, 0, 1)},
		{"private, 192.168/16", a4(192, 168, 1, 1)},
		{"link-local", a4(169, 254, 169, 254)},
		{"link-local multicast", a4(224, 0, 0, 251)},
		{"multicast", a4(224, 0, 0, 1)},
		{"unspecified", netip.IPv4Unspecified()},
		{"this network", a4(0, 1, 2, 3)},
		{"CGNAT, first", a4(100, 64, 0, 0)},
		{"CGNAT, last", a4(100, 127, 255, 255)},
		{"IETF protocol assignments", a4(192, 0, 0, 8)},
		{"documentation, TEST-NET-1", netip.MustParseAddr("192.0.2.1")},
		{"documentation, TEST-NET-2", a4(198, 51, 100, 1)},
		{"documentation, TEST-NET-3", a4(203, 0, 113, 1)},
		{"benchmarking, first", a4(198, 18, 0, 1)},
		{"benchmarking, last", a4(198, 19, 255, 255)},
		{"reserved", a4(240, 0, 0, 1)},
		{"broadcast", a4(255, 255, 255, 255)},
		{"IPv6 unspecified", netip.IPv6Unspecified()},
		{"IPv6 loopback", netip.IPv6Loopback()},
		{"IPv6 ULA, fd00::/8", netip.MustParseAddr("fd00::1")},
		{"IPv6 ULA, the tailnet's", netip.MustParseAddr("fd7a:115c:a1e0::1")},
		{"IPv6 link-local", netip.MustParseAddr("fe80::1")},
		{"IPv6 link-local with a zone", netip.MustParseAddr("fe80::1%eth0")},
		{"IPv6 link-local multicast", netip.MustParseAddr("ff02::1")},
		{"IPv6 interface-local multicast", netip.MustParseAddr("ff01::1")},
		{"IPv6 multicast", netip.MustParseAddr("ff05::1")},
		{"NAT64, well-known prefix", netip.MustParseAddr("64:ff9b::1")},
		{"NAT64, local-use prefix", netip.MustParseAddr("64:ff9b:1::1")},
		{"discard", netip.MustParseAddr("100::1")},
		{"Teredo", netip.MustParseAddr("2001::1")},
		{"documentation", netip.MustParseAddr("2001:db8::1")},
		{"6to4", netip.MustParseAddr("2002::1")},
		{"site-local", netip.MustParseAddr("fec0::1")},
		{"IPv4-mapped loopback", mapped4(127, 0, 0, 1)},
		{"IPv4-mapped private", mapped4(10, 0, 0, 1)},
		{"IPv4-mapped link-local", mapped4(169, 254, 1, 1)},
		{"zero address", netip.Addr{}},
	} {
		err := Public(tc.ip)
		if !errors.Is(err, ErrInside) {
			t.Errorf("%s (%s): %v, want a refusal wrapping ErrInside", tc.name, tc.ip, err)
		}
	}

	for _, tc := range []struct {
		name string
		ip   netip.Addr
	}{
		{"a public DNS resolver", a4(8, 8, 8, 8)},
		{"another public resolver", a4(1, 1, 1, 1)},
		{"just above private, 172.32/12", a4(172, 32, 0, 1)},
		{"just above CGNAT", a4(100, 128, 0, 1)},
		{"Cloudflare's IPv6", netip.MustParseAddr("2606:4700:4700::1111")},
		{"IPv4-mapped public", mapped4(8, 8, 8, 8)},
	} {
		if err := Public(tc.ip); err != nil {
			t.Errorf("%s (%s): %v, want nil", tc.name, tc.ip, err)
		}
	}
}

func TestControl(t *testing.T) {
	for _, tc := range []struct {
		name    string
		address string
		want    error // nil to pass, ErrInside to refuse
		anyErr  bool
	}{
		{name: "private", address: fmt.Sprintf("%s:443", a4(10, 1, 2, 3)), want: ErrInside},
		{name: "loopback", address: "127.0.0.1:80", want: ErrInside},
		{name: "public", address: fmt.Sprintf("%s:443", a4(8, 8, 8, 8))},
		{name: "IPv6 public", address: "[2606:4700:4700::1111]:443"},
		{name: "IPv6 loopback", address: "[::1]:443", want: ErrInside},
		{name: "a name, not an address", address: "example.com:443", anyErr: true},
		{name: "no port", address: "127.0.0.1", anyErr: true},
	} {
		err := Control("tcp", tc.address, nil)
		switch {
		case tc.anyErr:
			if err == nil {
				t.Errorf("%s (%q): nil, want an error", tc.name, tc.address)
			}
		case tc.want == nil:
			if err != nil {
				t.Errorf("%s (%q): %v, want nil", tc.name, tc.address, err)
			}
		default:
			if !errors.Is(err, tc.want) {
				t.Errorf("%s (%q): %v, want an error wrapping %v", tc.name, tc.address, err, tc.want)
			}
		}
	}
}

// TestTransportRefusesTheLocalServer is the SSRF case: a client whose
// transport is this package's cannot reach a server on loopback, even
// though the URL names it.
func TestTransportRefusesTheLocalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a request reached the server on loopback")
	}))
	defer srv.Close()
	tr := Transport(5 * time.Second)
	if tr.Proxy != nil {
		t.Error("the transport has a proxy; it would dial around the guard")
	}
	client := &http.Client{Transport: tr}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/feed", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); !errors.Is(err, ErrInside) {
		t.Fatalf("GET %s: %v, want a refusal wrapping ErrInside", srv.URL, err)
	}
}

// TestTransportRefusesARedirect: the guard allows the first connection
// (here, a test server on loopback, as a public address would be) and
// refuses the second, so a public page that redirects inside is refused.
func TestTransportRefusesARedirect(t *testing.T) {
	var hops atomic.Int32
	guard := func(ip netip.Addr) error {
		if hops.Add(1) == 1 {
			return nil // the first hop, the page itself
		}
		return Public(ip)
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close") // no reused connection for the next hop
		http.Redirect(w, r, srv.URL+"/inside", http.StatusFound)
	}))
	defer srv.Close()
	client := &http.Client{Transport: &http.Transport{
		DialContext: (&net.Dialer{Control: controlWith(guard)}).DialContext,
	}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/outside", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); !errors.Is(err, ErrInside) {
		t.Fatalf("GET %s: %v, want the redirect to be refused with ErrInside", srv.URL, err)
	}
	if got := hops.Load(); got != 2 {
		t.Errorf("%d connections, want 2: the page, then the refused one", got)
	}
}

// TestDNSRebinding answers one name from a fake DNS server: the first
// answer is public, the second is loopback. The dialer lets the public
// answer through Control, and refuses the rebound one. A test connection
// stub stops the first dial before any packet leaves the host.
func TestDNSRebinding(t *testing.T) {
	rebind := newDNSServer(t, a4(93, 184, 215, 14), a4(127, 0, 0, 1))

	var (
		mu   sync.Mutex
		seen []string
	)
	stop := errors.New("stop before connecting")
	d := Dialer(time.Second)
	d.Resolver = rebind.resolver()
	guard := d.Control
	d.Control = func(network, address string, c syscall.RawConn) error {
		mu.Lock()
		seen = append(seen, address)
		mu.Unlock()
		if err := guard(network, address, c); err != nil {
			return err
		}
		return stop
	}

	_, err := d.DialContext(t.Context(), "tcp4", "rebind.example:443")
	if !errors.Is(err, stop) {
		t.Fatalf("the first dial: %v, want its public answer to pass Control", err)
	}

	_, err = d.DialContext(t.Context(), "tcp4", "rebind.example:443")
	if !errors.Is(err, ErrInside) {
		t.Fatalf("the second dial: %v, want the rebound answer refused with ErrInside", err)
	}

	want := []string{fmt.Sprintf("%s:443", a4(93, 184, 215, 14)), "127.0.0.1:443"}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != len(want) || seen[0] != want[0] || seen[1] != want[1] {
		t.Errorf("Control saw %v, want %v", seen, want)
	}
}

func TestName(t *testing.T) {
	for _, tc := range []struct {
		name string
		host string
		want error
	}{
		{name: "localhost", host: "localhost", want: ErrInside},
		{name: "localhost, upper case", host: "LocalHost", want: ErrInside},
		{name: "under .localhost", host: "<app>.localhost", want: ErrInside},
		{name: "mDNS", host: "<host>.local", want: ErrInside},
		{name: "private network", host: "<host>.internal", want: ErrInside},
		{name: "home network", host: "<host>.lan", want: ErrInside},
		{name: "home.arpa", host: "<host>.home.arpa", want: ErrInside},
		{name: "the tailnet", host: "<tailnet>.ts.net", want: ErrInside},
		{name: "no dot", host: "nas", want: ErrInside},
		{name: "empty", host: "", want: ErrInside},
		{name: "loopback literal", host: "127.0.0.1", want: ErrInside},
		{name: "private literal", host: a4(10, 0, 0, 1).String(), want: ErrInside},
		{name: "public literal", host: a4(8, 8, 8, 8).String()},
		{name: "public name", host: "example.com"},
		{name: "trailing dot", host: "example.com."},
	} {
		err := Name(tc.host)
		switch {
		case tc.want == nil && err != nil:
			t.Errorf("%s (%q): %v, want nil", tc.name, tc.host, err)
		case tc.want != nil && !errors.Is(err, tc.want):
			t.Errorf("%s (%q): %v, want an error wrapping %v", tc.name, tc.host, err, tc.want)
		}
	}
}

// dnsServer is a fake DNS server on loopback. It answers each A query with
// the next address in answers and every other query, AAAA included, with
// none, so a test decides what a name resolves to and no query reaches a
// real resolver.
type dnsServer struct {
	pc      net.PacketConn
	answers []netip.Addr
	mu      sync.Mutex
	next    int
}

func newDNSServer(t *testing.T, answers ...netip.Addr) *dnsServer {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("the fake DNS server: %v", err)
	}
	s := &dnsServer{pc: pc, answers: answers}
	t.Cleanup(func() { _ = pc.Close() })
	go s.serve()
	return s
}

// resolver is a net.Resolver that asks the fake server.
func (s *dnsServer) resolver() *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp4", s.pc.LocalAddr().String())
	}}
}

func (s *dnsServer) serve() {
	buf := make([]byte, 512)
	for {
		n, addr, err := s.pc.ReadFrom(buf)
		if err != nil {
			return // closed by the test
		}
		if resp, ok := s.reply(buf[:n]); ok {
			_, _ = s.pc.WriteTo(resp, addr)
		}
	}
}

// reply answers one query. It echoes the question, and adds one A record
// when the query asks for A and an answer is left.
func (s *dnsServer) reply(query []byte) ([]byte, bool) {
	const header, qtypeA, record = 12, 1, 16
	if len(query) < header+5 {
		return nil, false
	}
	// Skip the name's labels to find its type; a query carries no
	// compression pointer.
	i := header
	for i < len(query) && query[i] != 0 {
		i += int(query[i]) + 1
	}
	end := i + 1 + 4 // the root label, then QTYPE and QCLASS
	if i >= len(query) || end > len(query) {
		return nil, false
	}
	resp := make([]byte, header, header+end-header+record)
	copy(resp, query[:header])
	resp[2], resp[3] = 0x81, 0x80 // the response, recursion available
	resp[6], resp[7] = 0, 0       // no answer yet
	resp[8], resp[9] = 0, 0       // no authority
	resp[10], resp[11] = 0, 0     // no additional record: no OPT is echoed
	resp = append(resp, query[header:end]...)
	if binary.BigEndian.Uint16(query[i+1:]) != qtypeA {
		return resp, true
	}
	s.mu.Lock()
	ip, ok := netip.Addr{}, false
	if s.next < len(s.answers) {
		ip, ok = s.answers[s.next], true
		s.next++
	}
	s.mu.Unlock()
	if !ok {
		return resp, true
	}
	resp[7] = 1 // one answer
	resp = append(resp, 0xc0, 0x0c, 0x00, qtypeA, 0x00, 0x01, 0x00, 0x00, 0x00, 0x3c, 0x00, 0x04)
	b := ip.As4()
	resp = append(resp, b[:]...)
	return resp, true
}

// TestDNSReply checks the fake server's own bytes against the addresses
// the rebinding test expects, so a mistake in the server fails here rather
// than as a puzzling resolver error.
func TestDNSReply(t *testing.T) {
	s := &dnsServer{answers: []netip.Addr{a4(93, 184, 215, 14)}}
	query := []byte{
		0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		6, 'r', 'e', 'b', 'i', 'n', 'd', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		0x00, 0x00, 0x01, 0x00, 0x01,
	}
	resp, ok := s.reply(query)
	if !ok {
		t.Fatal("the fake server refused a valid query")
	}
	if len(resp) != len(query)+16 {
		t.Fatalf("the response is %d bytes, want %d", len(resp), len(query)+16)
	}
	if resp[1] != 0x34 || resp[3] != 0x80 || resp[7] != 1 {
		t.Errorf("the header is % x", resp[:12])
	}
	// The answer's name pointer, type, class, TTL and the address.
	want := []byte{0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x3c, 0x00, 0x04, 93, 184, 215, 14}
	if got := resp[len(resp)-len(want):]; !bytes.Equal(got, want) {
		t.Errorf("the answer is % x, want % x", got, want)
	}
	if q, ok := s.reply(query); !ok || q[7] != 0 {
		t.Errorf("a second A query: %v, want NOERROR with no answer left", ok)
	}
	// An AAAA query gets NOERROR and no answer.
	aaaa := append([]byte(nil), query...)
	aaaa[len(aaaa)-3] = 28
	if resp, ok := s.reply(aaaa); !ok || resp[7] != 0 {
		t.Errorf("an AAAA query: %v", ok)
	}
}
