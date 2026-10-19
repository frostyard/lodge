package tailnet

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"

	"github.com/frostyard/lodge/internal/peer"
)

// startControl runs Tailscale's in-process test control server and a DERP
// server on loopback. No test here reaches the internet.
func startControl(t *testing.T, edit func(*testcontrol.Server)) (string, *testcontrol.Server) {
	t.Helper()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })
	derpMap := integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	control := &testcontrol.Server{
		DERPMap:        derpMap,
		DNSConfig:      &tailcfg.DNSConfig{Proxied: true},
		MagicDNSDomain: "tailnet.example",
		TagOwners:      map[string][]string{"tag:agent": nil},
		Logf:           logger.Discard,
	}
	if edit != nil {
		edit(control)
	}
	control.HTTPTestServer = httptest.NewUnstartedServer(control)
	control.HTTPTestServer.Start()
	t.Cleanup(control.HTTPTestServer.Close)
	return control.HTTPTestServer.URL, control
}

func startNode(t *testing.T, ctx context.Context, cfg Config) *Node {
	t.Helper()
	n, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}

func TestStateDirIs0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tailnet", "lodge")
	if err := stateDir(dir); err != nil {
		t.Fatal(err)
	}
	wide := filepath.Join(t.TempDir(), "wide")
	os.Mkdir(wide, 0o755)
	if err := stateDir(wide); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, wide} {
		fi, err := os.Stat(d)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: mode %v, %v; want 0700", d, fi.Mode().Perm(), err)
		}
	}
}

func selfSigned(t *testing.T, name string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// The listener offers HTTP/1.1 only, whatever the client offers, and
// serves only the node's own name.
func TestTLSIsHTTP1Only(t *testing.T) {
	cert := selfSigned(t, "lodge.tailnet.example")
	cfg := tlsConfig("lodge.tailnet.example", func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil })
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { c.(*tls.Conn).Handshake(); io.Copy(io.Discard, c) }()
		}
	}()
	dial := func(name string, protos []string) (*tls.Conn, error) {
		return tls.Dial("tcp", ln.Addr().String(), &tls.Config{ServerName: name, NextProtos: protos, InsecureSkipVerify: true})
	}
	c, err := dial("lodge.tailnet.example", []string{"h2", "http/1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if p := c.ConnectionState().NegotiatedProtocol; p != "http/1.1" {
		t.Errorf("negotiated %q, want http/1.1", p)
	}
	c.Close()
	if c, err := dial("lodge.tailnet.example", []string{"h2"}); err == nil {
		c.Close()
		t.Error("a client offering only h2 connected")
	}
	if c, err := dial("other.tailnet.example", nil); err == nil {
		c.Close()
		t.Error("another name got a certificate")
	}
}

func TestStartWithAuthKey(t *testing.T) {
	control, _ := startControl(t, func(c *testcontrol.Server) { c.RequireAuthKey = "tskey-auth-test" })
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "tailnet", "lodge")
	n := startNode(t, ctx, Config{Hostname: "lodge", Dir: dir, AuthKey: "tskey-auth-test", ControlURL: control})
	if !strings.HasPrefix(n.DNSName(), "lodge.") || strings.HasSuffix(n.DNSName(), ".") {
		t.Errorf("DNSName = %q", n.DNSName())
	}
	if !n.Running(ctx) {
		t.Error("the node is not running")
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state dir: %v %v", fi.Mode().Perm(), err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Error("the node keeps no state in its directory")
	}

	ctx2, cancel2 := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel2()
	if n, err := Start(ctx2, Config{Hostname: "other", Dir: t.TempDir(), AuthKey: "tskey-wrong", ControlURL: control}); err == nil {
		n.Close()
		t.Error("a wrong auth key started a node")
	}
}

// syncBuffer is an io.Writer for a logger that tests read concurrently.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Without an auth key, a node starts without waiting, reports that it
// needs a login with the URL, and logs the URL.
func TestStatusWhileNeedsLogin(t *testing.T) {
	control, _ := startControl(t, func(c *testcontrol.Server) { c.RequireAuth = true })
	n, err := New(Config{Hostname: "lodge", Dir: t.TempDir(), ControlURL: control})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	for {
		st := n.Status(ctx)
		if st.State == "NeedsLogin" && strings.HasPrefix(st.LoginURL, control) {
			if st.Hostname != "lodge" || st.DNSName != "" {
				t.Errorf("status = %+v", st)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no login URL in the status: %+v", st)
		case <-time.After(50 * time.Millisecond):
		}
	}
	if _, err := n.ListenTLS(); err == nil {
		t.Error("a node that is not up listened")
	}
}

// Without an auth key, the node logs Tailscale's login URL and waits.
func TestLoginURLLoggedWithoutKey(t *testing.T) {
	control, _ := startControl(t, func(c *testcontrol.Server) { c.RequireAuth = true })
	var logs syncBuffer
	logged := make(chan struct{})
	var once sync.Once
	h := slog.NewTextHandler(io.MultiWriter(&logs, writerFunc(func(p []byte) {
		if bytes.Contains(p, []byte("go to: http")) {
			once.Do(func() { close(logged) })
		}
	})), &slog.HandlerOptions{Level: slog.LevelInfo})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		n, err := Start(ctx, Config{Hostname: "lodge", Dir: t.TempDir(), ControlURL: control, Log: slog.New(h)})
		if n != nil {
			n.Close()
		}
		done <- err
	}()
	select {
	case <-logged:
	case <-time.After(time.Minute):
		t.Fatalf("no login URL logged; logs:\n%s", logs.String())
	}
	cancel()
	if err := <-done; err == nil {
		t.Error("Start returned without a login")
	}
	if !strings.Contains(logs.String(), control) {
		t.Errorf("the login URL is not the control server's: %s", logs.String())
	}
}

type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) { f(p); return len(p), nil }

func TestWhoIs(t *testing.T) {
	control, _ := startControl(t, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	n := startNode(t, ctx, Config{Hostname: "lodge", Dir: t.TempDir(), ControlURL: control})
	ln, err := n.srv.Listen("tcp", ":80")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := n.WhoIs(r.Context(), r.RemoteAddr)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		ip := net.ParseIP(strings.Trim(r.RemoteAddr[:strings.LastIndex(r.RemoteAddr, ":")], "[]"))
		owns := false
		for _, a := range p.Addrs {
			owns = owns || a.String() == ip.String()
		}
		io.WriteString(w, strings.Join([]string{p.NodeID, p.NodeName, p.Login, map[bool]string{true: "tagged", false: "user"}[p.Tagged],
			map[bool]string{true: "own", false: "foreign"}[owns]}, " "))
	}), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	ask := func(hostname string, tags []string) []string {
		t.Helper()
		c := &tsnet.Server{Dir: t.TempDir(), Hostname: hostname, ControlURL: control, AdvertiseTags: tags, Logf: logger.Discard, UserLogf: logger.Discard}
		defer c.Close()
		if _, err := c.Up(ctx); err != nil {
			t.Fatal(err)
		}
		hc := c.HTTPClient()
		var resp *http.Response
		// The new peer reaches lodge's node once the netmap reaches both.
		for {
			resp, err = hc.Get("http://" + n.DNSName() + "/")
			if err == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("%s cannot reach lodge: %v", hostname, err)
			case <-time.After(100 * time.Millisecond):
			}
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return strings.Fields(string(b))
	}
	got := ask("phone", nil)
	if len(got) != 5 || got[0] == "" || got[1] != "phone" || !strings.Contains(got[2], "@") || got[3] != "user" || got[4] != "own" {
		t.Errorf("the phone = %q", got)
	}
	got = ask("agent", []string{"tag:agent"})
	if len(got) != 5 || got[1] != "agent" || got[3] != "tagged" {
		t.Errorf("the tagged node = %q", got)
	}
	if _, err := n.WhoIs(ctx, "192.0.2.1:1234"); !errors.Is(err, peer.ErrUnknown) {
		t.Errorf("an unknown address: %v, want ErrUnknown", err)
	}
	peers, err := n.Peers(ctx)
	if err != nil || len(peers) == 0 {
		t.Errorf("Peers = %v, %v", peers, err)
	}
}

// Nothing dials Tailscale's log host (ADR-0004; M4 review M6). tsnet
// starts no log uploader inside a test binary, so a helper binary runs a
// node against the test control server, with HTTPS_PROXY pointing at a
// proxy that records every CONNECT. A plain tsnet.Server is seen
// uploading; lodge's node is not.
func TestNothingDialsTheLogHost(t *testing.T) {
	control, _ := startControl(t, nil)
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command to build the helper")
	}
	bin := filepath.Join(t.TempDir(), "lognode")
	build := exec.CommandContext(t.Context(), gobin, "build", "-o", bin, "./testdata/lognode")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the helper: %v\n%s", err, out)
	}
	// A plain tsnet.Server uploads within a few seconds of starting; the
	// helper holds it up until the proxy sees that.
	proxy, connects, seen := connectRecorder(t)
	cmd := exec.CommandContext(t.Context(), bin, "-control", control, "-dir", t.TempDir(), "-bare", "-hold", "2m")
	cmd.Env = proxyEnv(proxy)
	var out syncBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-seen:
	case <-time.After(90 * time.Second):
		t.Fatalf("a plain tsnet.Server dialed %q; the test cannot see uploads\n%s", connects(), out.String())
	}
	cmd.Process.Kill()
	cmd.Wait()
	if !containsHost(connects(), "log.tailscale.com") {
		t.Fatalf("a plain tsnet.Server dialed %q, not the log host", connects())
	}

	// lodge's node, held up past logtail's flush delay (2 s) and then
	// closed, which flushes too, dials nothing through the proxy.
	proxy, connects, _ = connectRecorder(t)
	cmd = exec.CommandContext(t.Context(), bin, "-control", control, "-dir", t.TempDir(), "-hold", "6s")
	cmd.Env = proxyEnv(proxy)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the helper: %v\n%s", err, b)
	}
	if got := connects(); len(got) != 0 {
		t.Errorf("lodge's node dialed %q\n%s", got, b)
	}
}

func proxyEnv(proxy string) []string {
	return append(os.Environ(), "HTTPS_PROXY="+proxy, "https_proxy="+proxy, "HTTP_PROXY="+proxy, "http_proxy="+proxy, "NO_PROXY=", "no_proxy=")
}

func containsHost(connects []string, host string) bool {
	for _, c := range connects {
		if strings.HasPrefix(c, host+":") {
			return true
		}
	}
	return false
}

// connectRecorder is an HTTP proxy that records each CONNECT's target and
// refuses it. seen is closed at the first CONNECT to Tailscale's log host.
func connectRecorder(t *testing.T) (string, func() []string, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	logHost := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(10 * time.Second))
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil {
					return
				}
				mu.Lock()
				seen = append(seen, req.Host)
				mu.Unlock()
				if strings.HasPrefix(req.Host, "log.tailscale.com:") {
					once.Do(func() { close(logHost) })
				}
				io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
			}()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return "http://" + ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}, logHost
}
