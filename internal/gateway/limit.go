package gateway

import (
	"net"
	"net/netip"
	"sync"
)

// Connection caps (docs/specs/lodge-http.md, Connections).
const (
	// PerPeerConns bounds one remote address's open connections to one
	// listener.
	PerPeerConns = 64
	// MaxConns bounds one listener's open connections.
	MaxConns = 1024
)

// Limit wraps ln so that it holds at most perPeer connections from one
// remote IP and total in all. A connection past either is closed at
// once, before lodge reads anything from it.
func Limit(ln net.Listener, perPeer, total int) net.Listener {
	return &limitListener{Listener: ln, perPeer: perPeer, total: total, byIP: map[netip.Addr]int{}}
}

type limitListener struct {
	net.Listener
	perPeer, total int

	mu   sync.Mutex
	n    int
	byIP map[netip.Addr]int
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip := netip.Addr{}
		if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil {
			ip = ap.Addr().Unmap()
		}
		l.mu.Lock()
		if l.n >= l.total || l.byIP[ip] >= l.perPeer {
			l.mu.Unlock()
			_ = c.Close()
			continue
		}
		l.n++
		l.byIP[ip]++
		l.mu.Unlock()
		return &limitConn{Conn: c, release: func() { l.release(ip) }}, nil
	}
}

func (l *limitListener) release(ip netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.n--
	if l.byIP[ip]--; l.byIP[ip] <= 0 {
		delete(l.byIP, ip)
	}
}

type limitConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
