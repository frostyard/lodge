package gateway

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/frostyard/lodge/internal/apptest"
	"github.com/frostyard/lodge/kit/lodgeapp"
)

// serveKitApp serves a real lodgeapp.Server as app id under the harness's
// root: lodge and the kit must agree on every caller kind, header and
// path, and on the socket's peer check.
func (h *harness) serveKitApp(id string) {
	t := h.t
	t.Helper()
	apptest.Install(t, h.root, id, map[string]any{"name": "Kit", "content": true, "device": true, "mcp": true}, nil)
	env := lodgeapp.Env{Root: h.root, LodgeUID: os.Getuid()}
	ln, err := env.Listen(id)
	if err != nil {
		t.Fatal(err)
	}
	who := func(w http.ResponseWriter, r *http.Request) {
		c, _ := lodgeapp.CallerFrom(r.Context())
		io.WriteString(w, string(c.Kind)+" "+c.Name()+" "+c.Login+" "+c.DisplayName+" "+c.Scope+" "+r.URL.Path)
	}
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
		X: http.HandlerFunc(who), Content: http.HandlerFunc(who), MCP: http.HandlerFunc(who), Device: http.HandlerFunc(who),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("the kit's server: %v", err)
		}
	})
	if err := h.apps.Load(); err != nil {
		t.Fatal(err)
	}
	h.apps.Poll(t.Context())
	if a, _ := h.apps.Get(id); a.State != "up" {
		t.Fatalf("the kit app is %s: %s", a.State, a.Reason)
	}
}

func TestKitAppBehindTheGateway(t *testing.T) {
	h := newHarness(t)
	h.serveKitApp("kit")
	if a := h.get("/app/kit/x/a?b=1", nil); a.body != "session phone alice@example.com Alice Zoë  /x/a" {
		t.Errorf("x/: %d %q", a.status, a.body)
	}
	if a := h.contentGet("/kit/blob", nil); a.body != "content phone alice@example.com Alice Zoë  /content/blob" {
		t.Errorf("content: %d %q", a.status, a.body)
	}
	secret := h.issue("agent", "kit", "vault=1")
	if a := h.do(h.mcpReq(http.MethodPost, "kit", secret, nil)); a.body != "agent agent   vault=1 /mcp" {
		t.Errorf("mcp: %d %q", a.status, a.body)
	}
	if a := h.do(h.req(http.MethodPost, "/device/kit/pair", nil)); a.body != "device phone alice@example.com   /device/pair" {
		t.Errorf("device: %d %q", a.status, a.body)
	}
	c, _, err := h.wsDial("/app/kit/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","id":1,"method":"rpc.ping"}`)); err != nil {
		t.Fatal(err)
	}
	_, got, err := c.Read(ctx)
	if err != nil || strings.TrimSuffix(string(got), "\n") != `alice@example.com phone {"jsonrpc":"2.0","id":1,"method":"rpc.ping"}` {
		t.Errorf("api: %q %v", got, err)
	}
}
