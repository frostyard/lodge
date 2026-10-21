package inference

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/lodge/internal/store"
)

// TestCopilotLive checks the copilot provider against the owner's real
// plan: LODGE_LIVE_COPILOT=1, LODGE_COPILOT_TOKEN_FILE=<the GitHub token
// file from lodgectl provider login NAME --copilot>, and optionally
// LODGE_COPILOT_MODEL. It exchanges the token, lists the plan's models,
// and sends one small chat completion and one with a tool.
func TestCopilotLive(t *testing.T) {
	if os.Getenv("LODGE_LIVE_COPILOT") != "1" {
		t.Skip("set LODGE_LIVE_COPILOT=1 and LODGE_COPILOT_TOKEN_FILE to check the owner's Copilot plan")
	}
	raw, err := os.ReadFile(os.Getenv("LODGE_COPILOT_TOKEN_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := New(Options{DB: db, Callers: fakeCallers{os.Getuid(): "porter"}, DevUID: -1,
		Log: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	cfg := Config{Providers: map[string]ProviderConfig{"copilot": {Type: TypeCopilot, Slots: 1}},
		Aliases: map[string]AliasConfig{"smart": {Provider: "copilot", Model: "unset"}}}
	s.Load(cfg, nil)
	if err := s.SetKey(t.Context(), "copilot", strings.TrimSpace(string(raw))); err != nil {
		t.Fatal(err)
	}
	models, err := s.Models(t.Context(), "copilot")
	if err != nil || len(models) == 0 {
		t.Fatalf("models: %+v %v", models, err)
	}
	t.Logf("the plan's models: %+v", models)
	model := os.Getenv("LODGE_COPILOT_MODEL")
	if model == "" {
		model = models[0].ID
	}
	cfg.Aliases["smart"] = AliasConfig{Provider: "copilot", Model: model}
	s.Load(cfg, nil)
	s.CheckAll(t.Context())
	if st, _ := s.Status(); !st[0].Available {
		t.Fatalf("status: %+v", st[0])
	}

	sock := filepath.Join(dir, "inference.sock")
	ln, err := Listen(sock, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	h := &harness{t: t, s: s, client: &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}}}
	r := h.do(http.MethodPost, "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"Answer with the single word: pong"}]}`, nil)
	if r.status != 200 || !strings.Contains(strings.ToLower(r.body), "pong") {
		t.Fatalf("chat: %d %s", r.status, r.body)
	}
	r = h.do(http.MethodPost, "/v1/chat/completions", `{"model":"smart","tool_choice":"required",`+
		`"messages":[{"role":"user","content":"What time is it? Use the tool."}],`+
		`"tools":[{"type":"function","function":{"name":"porter__now","description":"The time now.","parameters":{"type":"object","properties":{}}}}]}`, nil)
	if r.status != 200 || !strings.Contains(r.body, "porter__now") {
		t.Fatalf("a tool call: %d %s", r.status, r.body)
	}
	sums, err := s.Usage(t.Context(), time.Time{})
	if err != nil || len(sums) != 1 || sums[0].Input <= 0 {
		t.Errorf("usage: %+v %v", sums, err)
	}
}
