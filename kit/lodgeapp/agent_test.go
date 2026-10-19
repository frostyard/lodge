package lodgeapp

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AgentClient dials the agent socket under Root, and sends its token as
// the bearer of every request.
func TestAgentClient(t *testing.T) {
	root, err := os.MkdirTemp("", "lr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	e := Env{Root: root, LodgeUID: os.Getuid()}
	path := e.Path(AgentSocket)
	if path != filepath.Join(root, "run/lodge/agent.sock") {
		t.Fatalf("the socket is %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	c := e.AgentClient("lgm_secret")
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, AgentURL("notes"), strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer someone-else")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "POST /mcp/notes Bearer lgm_secret" {
		t.Errorf("the socket saw %q", b)
	}
	if req.Header.Get("Authorization") != "Bearer someone-else" {
		t.Error("the caller's request was changed")
	}
}
