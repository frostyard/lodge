package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"

	"github.com/frostyard/lodge/internal/apptest"
	"github.com/frostyard/lodge/internal/control"
	"github.com/frostyard/lodge/kit/lodgeapp"
)

var builds struct {
	once     sync.Once
	dir      string
	release  string
	e2e      string
	err      error
	messages string
}

func TestMain(m *testing.M) {
	code := m.Run()
	if builds.dir != "" {
		os.RemoveAll(builds.dir)
	}
	os.Exit(code)
}

// binaries builds cmd/lodge twice, as a release and as the development
// build (-tags e2e), once per test run.
func binaries(t *testing.T) (release, e2e string) {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command")
	}
	builds.once.Do(func() {
		builds.dir, builds.err = os.MkdirTemp("", "lodge-build-")
		if builds.err != nil {
			return
		}
		builds.release = filepath.Join(builds.dir, "lodge")
		builds.e2e = filepath.Join(builds.dir, "lodge-e2e")
		for _, b := range []struct{ out, tags string }{{builds.release, ""}, {builds.e2e, "e2e"}} {
			args := []string{"build", "-trimpath", "-o", b.out}
			if b.tags != "" {
				args = append(args, "-tags", b.tags)
			}
			cmd := exec.Command(gobin, append(args, ".")...)
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				builds.err, builds.messages = err, string(out)
				return
			}
		}
	})
	if builds.err != nil {
		t.Fatalf("build: %v\n%s", builds.err, builds.messages)
	}
	return builds.release, builds.e2e
}

func symbols(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command("go", "tool", "nm", bin).Output()
	if err != nil {
		t.Fatalf("go tool nm %s: %v", bin, err)
	}
	return string(out)
}

// A release build holds no e2e identity code (docs/specs/lodge-http.md,
// The development build). The development build is checked too, so the
// test would see the code if it were there.
func TestReleaseBuildHoldsNoE2EIdentity(t *testing.T) {
	release, e2e := binaries(t)
	const pkg = "github.com/frostyard/lodge/internal/e2eid."
	if !strings.Contains(symbols(t, e2e), pkg) {
		t.Fatalf("the development build lists no %s symbol; the check cannot see it", pkg)
	}
	for line := range strings.SplitSeq(symbols(t, release), "\n") {
		if strings.Contains(line, pkg) {
			t.Errorf("the release build holds %s", strings.TrimSpace(line))
		}
	}
	b, err := os.ReadFile(release)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("e2e@example.com")) {
		t.Error("the release build holds the e2e login")
	}
}

// A release build's plain listener names no one: a browser there is
// refused, whatever it sends.
func TestReleasePlainListenerRefusesBrowsers(t *testing.T) {
	release, _ := binaries(t)
	l := startLodge(t, release, nil, "--node", "", "--listen", "127.0.0.1:0")
	resp, err := http.Get("http://" + l.lodge + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || strings.TrimSpace(string(b)) != "lodge cannot name this peer" {
		t.Errorf("a browser on a release build's plain listener: %d %q", resp.StatusCode, b)
	}
}

func TestListenMustBeLoopback(t *testing.T) {
	release, _ := binaries(t)
	for _, addr := range []string{"0.0.0.0:8080", "192.0.2.1:8080", ":8080", "localhost:8080"} {
		out, err := exec.Command(release, "--node", "", "--listen", addr).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "loopback") {
			t.Errorf("--listen %s: %v %q", addr, err, out)
		}
	}
}

type running struct {
	root, lodge, content string
}

var listening = regexp.MustCompile(`msg=listening listener="plain (lodge|content)" addr=(\S+)`)

// startLodge runs bin with args under a new root, after setup prepares
// it, until the test ends, and returns the plain listeners' addresses.
func startLodge(t *testing.T, bin string, setup func(root string), args ...string) running {
	t.Helper()
	r := running{root: t.TempDir()}
	apptest.InstallShell(t, r.root, nil)
	if setup != nil {
		setup(r.root)
	}
	cmd := exec.Command(bin, append([]string{"--root", r.root}, args...)...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	var mu sync.Mutex
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stderr)
		want := strings.Count(strings.Join(args, " "), "listen ")
		for sc.Scan() {
			mu.Lock()
			logs.WriteString(sc.Text() + "\n")
			if m := listening.FindStringSubmatch(sc.Text()); m != nil {
				if m[1] == "lodge" {
					r.lodge = m[2]
				} else {
					r.content = m[2]
				}
				if want--; want == 0 {
					close(ready)
				}
			}
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		if err := cmd.Wait(); err != nil {
			mu.Lock()
			t.Errorf("lodge exited with %v\n%s", err, logs.String())
			mu.Unlock()
		}
	})
	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("lodge did not listen:\n%s", logs.String())
	}
	mu.Lock()
	defer mu.Unlock()
	return r
}

// The development build serves an app end to end on loopback, with its
// fixed identity going through the allow-lists like any other.
func TestDevelopmentBuildEndToEnd(t *testing.T) {
	_, e2e := binaries(t)
	l := startLodge(t, e2e, func(root string) {
		apptest.Install(t, root, "fake", map[string]any{"name": "Fake", "key": "f", "content": true, "mcp": true}, nil)
		serveKitApp(t, root, "fake")
	}, "--listen", "127.0.0.1:0", "--content-listen", "127.0.0.1:0")

	get := func(addr, path string, hdr map[string]string) (int, http.Header, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
		for k, v := range hdr {
			if k == "Host" {
				req.Host = v
			} else {
				req.Header.Set(k, v)
			}
		}
		req.Close = true
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(b)
	}
	if code, _, body := get(l.lodge, "/", nil); code != http.StatusForbidden ||
		strings.TrimSpace(body) != "login e2e@example.com is not allowed: lodgectl allow login e2e@example.com" {
		t.Fatalf("before allowing: %d %q", code, body)
	}
	c, err := control.Dial(t.Context(), filepath.Join(l.root, "run/lodge/control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for method, p := range map[string]map[string]string{"lodge.allowLogin": {"login": "e2e@example.com"}, "lodge.allowNode": {"node": "e2e-node"}} {
		if err := c.Call(t.Context(), method, p, nil); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}
	if err := c.Call(t.Context(), "lodge.reload", nil, nil); err != nil {
		t.Fatal(err)
	}

	if code, _, body := get(l.lodge, "/fake/a/b", nil); code != http.StatusOK || !strings.Contains(body, "shell") {
		t.Errorf("the shell page: %d %q", code, body)
	}
	if code, _, _ := get(l.lodge, "/", map[string]string{"Host": "lodge.example"}); code != http.StatusForbidden {
		t.Errorf("another Host: %d", code)
	}
	if code, _, _ := get(l.lodge, "/lodge/apps", map[string]string{"Origin": "https://lodge.example"}); code != http.StatusForbidden {
		t.Errorf("another Origin: %d", code)
	}
	code, hdr, body := get(l.lodge, "/lodge/apps", map[string]string{"Origin": "http://" + l.lodge})
	if code != http.StatusOK || !strings.Contains(body, `"id":"fake"`) || !strings.Contains(body, `"state":"up"`) ||
		!strings.Contains(body, `"contentOrigin":"http://`+l.content+`"`) {
		t.Errorf("/lodge/apps: %d %q", code, body)
	}
	if !strings.Contains(hdr.Get("Content-Security-Policy"), "script-src http://"+l.lodge+"/lodge/") {
		t.Errorf("the shell policy: %q", hdr.Get("Content-Security-Policy"))
	}
	_, hdr, _ = get(l.lodge, "/app/fake/", nil)
	if !strings.Contains(hdr.Get("Content-Security-Policy"), "connect-src http://"+l.lodge+"/app/fake/ ws://"+l.lodge+"/app/fake/api") {
		t.Errorf("the app policy: %q", hdr.Get("Content-Security-Policy"))
	}
	if code, _, body := get(l.lodge, "/app/fake/x/whoami", nil); code != http.StatusOK || body != "session e2e e2e@example.com E2E /x/whoami" {
		t.Errorf("x/: %d %q", code, body)
	}
	code, hdr, body = get(l.content, "/fake/blob", map[string]string{"Sec-Fetch-Site": "same-site"})
	if code != http.StatusOK || body != "content e2e e2e@example.com E2E /content/blob" ||
		!strings.Contains(hdr.Get("Content-Security-Policy"), "sandbox; frame-ancestors http://"+l.lodge) {
		t.Errorf("content: %d %q %q", code, body, hdr.Values("Content-Security-Policy"))
	}

	dctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	h := http.Header{}
	h.Set("Origin", "http://"+l.lodge)
	ws, _, err := websocket.Dial(dctx, "ws://"+l.lodge+"/app/fake/api", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if err := ws.Write(dctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","id":1,"method":"rpc.ping"}`)); err != nil {
		t.Fatal(err)
	}
	_, got, err := ws.Read(dctx)
	if err != nil || strings.TrimSpace(string(got)) != `e2e@example.com e2e {"jsonrpc":"2.0","id":1,"method":"rpc.ping"}` {
		t.Errorf("the session: %q %v", got, err)
	}
	ws.Close(websocket.StatusNormalClosure, "")
}

// serveKitApp serves a lodgeapp.Server as app id under root.
func serveKitApp(t *testing.T, root, id string) {
	t.Helper()
	ln, err := lodgeapp.Env{Root: root, LodgeUID: os.Getuid()}.Listen(id)
	if err != nil {
		t.Fatal(err)
	}
	who := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := lodgeapp.CallerFrom(r.Context())
		io.WriteString(w, string(c.Kind)+" "+c.Name()+" "+c.Login+" "+c.DisplayName+" "+r.URL.Path)
	})
	srv := &lodgeapp.Server{
		Health: func(context.Context) lodgeapp.Health { return lodgeapp.Health{OK: true, Version: "1", Protocol: 1} },
		API: func(ctx context.Context, nc net.Conn, c lodgeapp.Caller) {
			sc := bufio.NewScanner(nc)
			for sc.Scan() {
				if _, err := io.WriteString(nc, c.Login+" "+c.NodeName+" "+sc.Text()+"\n"); err != nil {
					return
				}
			}
		},
		X: who, Content: who, MCP: who,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

// The control socket and the health polls run while the nodes wait for a
// login, and lodge.status shows each node's login URL: the first start
// without an auth key is approve the nodes, then lodgectl allow.
func TestControlWhileNodesNeedLogin(t *testing.T) {
	release, _ := binaries(t)
	controlURL := startTestControl(t)
	root := t.TempDir()
	cmd := exec.Command(release, "--root", root)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TS_") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "TS_CONTROL_URL="+controlURL)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stderr)
		once := sync.Once{}
		for sc.Scan() {
			logs.Write([]byte(sc.Text() + "\n"))
			if strings.Contains(sc.Text(), "control socket ready") {
				once.Do(func() { close(ready) })
			}
		}
	}()
	defer func() {
		cmd.Process.Signal(syscall.SIGTERM)
		if err := cmd.Wait(); err != nil {
			t.Errorf("lodge, stopped while it waited for a login, exited with %v\n%s", err, logs.String())
		}
	}()
	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatalf("no control socket while the nodes wait:\n%s", logs.String())
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	c, err := control.Dial(ctx, filepath.Join(root, "run/lodge/control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call(ctx, "lodge.allowLogin", map[string]string{"login": "alice@example.com"}, nil); err != nil {
		t.Errorf("allow login while the nodes wait: %v", err)
	}
	if err := c.Call(ctx, "lodge.allowNode", map[string]string{"node": "nPhone1CNTRL"}, nil); err != nil {
		t.Errorf("allow a node by ID while the nodes wait: %v", err)
	}
	for {
		var st control.Status
		if err := c.Call(ctx, "lodge.status", nil, &st); err != nil {
			t.Fatal(err)
		}
		waiting := 0
		for _, n := range st.Nodes {
			if n.State == "NeedsLogin" && strings.HasPrefix(n.LoginURL, controlURL) && !n.Up {
				waiting++
			}
		}
		if len(st.Nodes) == 2 && waiting == 2 {
			if st.Nodes[0].Name != "lodge" || st.Nodes[1].Name != "lodge-content" {
				t.Errorf("nodes = %+v", st.Nodes)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the status never showed both nodes needing a login: %+v\n%s", st, logs.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

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

// startTestControl runs Tailscale's in-process test control server, which
// requires an interactive login, and a DERP server, on loopback.
func startTestControl(t *testing.T) string {
	t.Helper()
	derpMap := integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	c := &testcontrol.Server{DERPMap: derpMap, DNSConfig: &tailcfg.DNSConfig{Proxied: true}, MagicDNSDomain: "tailnet.example",
		RequireAuth: true, Logf: logger.Discard}
	c.HTTPTestServer = httptest.NewUnstartedServer(c)
	c.HTTPTestServer.Start()
	t.Cleanup(c.HTTPTestServer.Close)
	return c.HTTPTestServer.URL
}
