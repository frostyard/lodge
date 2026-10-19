// Package tailnet runs lodge's tailnet nodes, lodge and lodge-content, in
// the lodge process with tsnet (ADR-0004; docs/specs/lodge-http.md,
// Listeners). It is the only package that imports tailscale.com. Each
// node keeps its state in lodge's state directory, serves TLS with its own
// certificate on port 443 and HTTP/1.1 only, and names peers with WhoIs.
// Tailscale's log upload is disabled before any node starts.
package tailnet

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"

	"tailscale.com/client/local"
	"tailscale.com/logtail"
	"tailscale.com/tsnet"

	"github.com/frostyard/lodge/internal/peer"
)

// Config is one node's.
type Config struct {
	// Hostname is the node's name on the tailnet: lodge or lodge-content.
	Hostname string
	// Dir is the node's state directory, /var/lib/lodge/tailnet/<node>/.
	// Start creates it, or narrows it, to 0700.
	Dir string
	// AuthKey is read on the first start only (TS_AUTHKEY, or
	// TS_AUTHKEY_CONTENT for the content node). Without one, the node logs
	// Tailscale's login URL and waits.
	AuthKey string
	// ControlURL is Tailscale's when empty; tests use their own.
	ControlURL string
	Log        *slog.Logger // [discard]
}

// Node is one running tailnet node.
type Node struct {
	srv      *tsnet.Server
	lc       *local.Client
	hostname string
	log      *slog.Logger

	mu       sync.Mutex
	name     string // the node's full DNS name, without the final dot, once up
	loginURL string // the latest login URL tsnet announced
}

// DisableLogUpload turns off Tailscale's log upload for the process. Start
// calls it before every node starts; it is exported for the test that
// checks it.
func DisableLogUpload() { logtail.Disable() }

// New starts a node without waiting for it to join the tailnet, so lodge
// can serve its control socket meanwhile; Up waits. Without an auth key the
// node needs a login, and Status shows the URL.
func New(cfg Config) (*Node, error) {
	DisableLogUpload()
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Hostname == "" || cfg.Dir == "" {
		return nil, errors.New("tailnet: a node needs a hostname and a state directory")
	}
	if err := stateDir(cfg.Dir); err != nil {
		return nil, err
	}
	log := cfg.Log.With("node", cfg.Hostname)
	n := &Node{hostname: cfg.Hostname, log: log}
	srv := &tsnet.Server{
		Dir:        cfg.Dir,
		Hostname:   cfg.Hostname,
		AuthKey:    cfg.AuthKey,
		ControlURL: cfg.ControlURL,
		Logf:       func(format string, args ...any) { log.Debug(fmt.Sprintf(format, args...)) },
		// The login URL, when there is no auth key, comes this way, every
		// 5 s: it is logged once per URL and kept for Status.
		UserLogf: n.userLog,
	}
	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("tailnet node %s: %w", cfg.Hostname, err)
	}
	lc, err := srv.LocalClient()
	if err != nil {
		_ = srv.Close()
		return nil, fmt.Errorf("tailnet node %s: %w", cfg.Hostname, err)
	}
	n.srv, n.lc = srv, lc
	return n, nil
}

// loginLine is how tsnet announces the login URL.
const loginLine = "go to: "

// userLog logs tsnet's messages for people, but the login URL only when it
// changes.
func (n *Node) userLog(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if _, url, ok := strings.Cut(msg, loginLine); ok {
		url = strings.TrimSpace(url)
		n.mu.Lock()
		seen := n.loginURL == url
		n.loginURL = url
		n.mu.Unlock()
		if !seen {
			n.log.Warn("the tailnet node needs a login: open the URL, or restart with an auth key", "url", url)
		}
		return
	}
	n.log.Info(msg)
}

// Up waits until the node is up on the tailnet, and learns its DNS name.
func (n *Node) Up(ctx context.Context) error {
	status, err := n.srv.Up(ctx)
	if err != nil {
		return fmt.Errorf("tailnet node %s: %w", n.hostname, err)
	}
	name := strings.TrimSuffix(status.Self.DNSName, ".")
	if domains := n.srv.CertDomains(); len(domains) > 0 {
		name = domains[0]
	}
	n.mu.Lock()
	n.name = name
	n.mu.Unlock()
	n.log.Info("tailnet node up", "name", name)
	return nil
}

// Start starts a node and waits until it is up on the tailnet.
func Start(ctx context.Context, cfg Config) (*Node, error) {
	n, err := New(cfg)
	if err != nil {
		return nil, err
	}
	if err := n.Up(ctx); err != nil {
		_ = n.Close()
		return nil, err
	}
	return n, nil
}

// Status is a node's state for lodgectl status.
type Status struct {
	// Hostname is the node's name; DNSName its full name once up.
	Hostname string `json:"hostname"`
	DNSName  string `json:"dnsName,omitzero"`
	// State is tailscaled's backend state: NoState, NeedsLogin,
	// NeedsMachineAuth, Starting, Running or Stopped.
	State string `json:"state"`
	// LoginURL is where to log the node in, while it needs a login.
	LoginURL string `json:"loginURL,omitzero"`
}

// Status reports the node's state, and its login URL while it needs one.
func (n *Node) Status(ctx context.Context) Status {
	st := Status{Hostname: n.hostname, DNSName: n.DNSName(), State: "Unknown"}
	n.mu.Lock()
	announced := n.loginURL
	n.mu.Unlock()
	s, err := n.lc.StatusWithoutPeers(ctx)
	if err != nil {
		st.LoginURL = announced
		return st
	}
	st.State = s.BackendState
	if s.BackendState != "Running" {
		st.LoginURL = cmp.Or(s.AuthURL, announced)
	}
	return st
}

// stateDir creates dir 0700, or narrows an existing one to 0700.
func stateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("tailnet state: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("tailnet state: %w", err)
	}
	return nil
}

// DNSName is the node's full DNS name, LODGE or CONTENT, once it is up.
func (n *Node) DNSName() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.name
}

// ListenTLS listens on port 443 with the node's certificate, offering
// HTTP/1.1 only.
func (n *Node) ListenTLS() (net.Listener, error) {
	name := n.DNSName()
	if name == "" {
		return nil, fmt.Errorf("tailnet node %s: not up yet", n.hostname)
	}
	ln, err := n.srv.Listen("tcp", ":443")
	if err != nil {
		return nil, fmt.Errorf("tailnet node %s: listen: %w", name, err)
	}
	return tls.NewListener(ln, tlsConfig(name, n.lc.GetCertificate)), nil
}

// tlsConfig serves name's certificate, from get, and nothing else, with
// HTTP/1.1 as the only application protocol.
func tlsConfig(name string, get func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(hi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if !strings.EqualFold(hi.ServerName, name) {
				return nil, fmt.Errorf("no certificate for %q", hi.ServerName)
			}
			return get(hi)
		},
	}
}

// WhoIs names the peer at remoteAddr.
func (n *Node) WhoIs(ctx context.Context, remoteAddr string) (peer.Peer, error) {
	res, err := n.lc.WhoIs(ctx, remoteAddr)
	if errors.Is(err, local.ErrPeerNotFound) || (err == nil && (res.Node == nil || res.UserProfile == nil)) {
		return peer.Peer{}, peer.ErrUnknown
	}
	if err != nil {
		return peer.Peer{}, fmt.Errorf("whois: %w", err)
	}
	p := peer.Peer{
		NodeID:      string(res.Node.StableID),
		NodeName:    firstLabel(res.Node.Name),
		Tagged:      res.Node.IsTagged(),
		Login:       res.UserProfile.LoginName,
		DisplayName: res.UserProfile.DisplayName,
	}
	for _, pfx := range res.Node.Addresses {
		if pfx.IsSingleIP() {
			p.Addrs = append(p.Addrs, pfx.Addr())
		}
	}
	return p, nil
}

// Peers lists the node's peers, for lodgectl allow node NAME.
func (n *Node) Peers(ctx context.Context) ([]peer.Peer, error) {
	st, err := n.lc.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	out := make([]peer.Peer, 0, len(st.Peer))
	for _, ps := range st.Peer {
		out = append(out, peer.Peer{
			NodeID:   string(ps.ID),
			NodeName: firstLabel(ps.DNSName),
			Addrs:    ps.TailscaleIPs,
			Tagged:   ps.Tags != nil && ps.Tags.Len() > 0,
		})
	}
	return out, nil
}

// Running reports whether the node is up on the tailnet.
func (n *Node) Running(ctx context.Context) bool {
	st, err := n.lc.StatusWithoutPeers(ctx)
	return err == nil && st.BackendState == "Running"
}

// Close stops the node.
func (n *Node) Close() error { return n.srv.Close() }

// firstLabel is a DNS name's first label.
func firstLabel(name string) string {
	label, _, _ := strings.Cut(name, ".")
	return strings.ToLower(label)
}
