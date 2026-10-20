package inference

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/lodge/internal/codexauth"
	"github.com/frostyard/lodge/internal/store"
)

// TestCodexLive checks the codex provider against the owner's real plan:
// LODGE_LIVE_CODEX=1, LODGE_CODEX_LOGIN=<a login file from lodgectl
// provider login>, and optionally LODGE_CODEX_MODEL. It never refreshes
// the login, whose refresh token belongs to lodge's host: it skips when
// the access token is due.
func TestCodexLive(t *testing.T) {
	if os.Getenv("LODGE_LIVE_CODEX") != "1" {
		t.Skip("set LODGE_LIVE_CODEX=1 and LODGE_CODEX_LOGIN to check the owner's plan")
	}
	raw, err := os.ReadFile(os.Getenv("LODGE_CODEX_LOGIN"))
	if err != nil {
		t.Fatal(err)
	}
	var l codexauth.Login
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	if l.Due(time.Now()) {
		t.Skip("the access token is due: sign in again, since this test never refreshes")
	}
	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := New(Options{DB: db, Callers: fakeCallers{os.Getuid(): "porter"}, DevUID: -1,
		Log: slog.New(slog.NewTextHandler(os.Stderr, nil)), Auth: codexauth.Endpoints{Token: "http://127.0.0.1:1/never"}})
	cfg := Config{Providers: map[string]ProviderConfig{"plan": {Type: TypeCodex, Slots: 1}},
		Aliases: map[string]AliasConfig{"smart": {Provider: "plan", Model: "unset"}}}
	s.Load(cfg, nil)
	if err := s.ImportLogin(t.Context(), "plan", l); err != nil {
		t.Fatal(err)
	}
	models, err := s.Models(t.Context(), "plan")
	if err != nil || len(models) == 0 {
		t.Fatalf("models: %+v %v", models, err)
	}
	t.Logf("the plan's models: %+v", models)
	model := os.Getenv("LODGE_CODEX_MODEL")
	if model == "" {
		model = models[0].ID
	}
	cfg.Aliases["smart"] = AliasConfig{Provider: "plan", Model: model}
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

	r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"smart","messages":[{"role":"system","content":"Answer in one word."},`+
		`{"role":"user","content":"What color is a clear daytime sky?"}]}`, nil)
	if r.status != 200 || !strings.Contains(strings.ToLower(r.body), "blue") {
		t.Fatalf("a plain chat: %d %s", r.status, r.body)
	}

	tools := `"tools":[{"type":"function","function":{"name":"notes__read_note","description":"Read a note by its path.",` +
		`"parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}},` +
		`{"type":"function","function":{"name":"porter__now","description":"The current time.","parameters":{"type":"object","properties":{}}}}]`
	r = h.do(http.MethodPost, "/v1/chat/completions", `{"model":"smart","tool_choice":"required",`+tools+`,"messages":[`+
		`{"role":"system","content":"Use the tools."},{"role":"user","content":"What does the note Inbox.md say?"}]}`, nil)
	var got struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if r.status != 200 || json.Unmarshal([]byte(r.body), &got) != nil || len(got.Choices) == 0 || len(got.Choices[0].Message.ToolCalls) == 0 {
		t.Fatalf("a tool call: %d %s", r.status, r.body)
	}
	call := got.Choices[0].Message.ToolCalls[0]
	if call.Function.Name != "notes__read_note" || !strings.Contains(call.Function.Arguments, "Inbox.md") {
		t.Fatalf("the tool call: %+v", call)
	}

	followUp := `{"model":"smart",` + tools + `,"messages":[{"role":"system","content":"Use the tools."},` +
		`{"role":"user","content":"What does the note Inbox.md say?"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"` + call.ID + `","type":"function","function":{"name":"notes__read_note","arguments":` +
		jsonString(call.Function.Arguments) + `}}]},` +
		`{"role":"tool","tool_call_id":"` + call.ID + `","content":"Buy oat milk."}]}`
	r = h.do(http.MethodPost, "/v1/chat/completions", followUp, nil)
	if r.status != 200 || !strings.Contains(strings.ToLower(r.body), "oat milk") {
		t.Fatalf("the answer after the tool's result: %d %s", r.status, r.body)
	}

	r = h.do(http.MethodPost, "/v1/chat/completions", `{"model":"smart","stream":true,"stream_options":{"include_usage":true},`+
		`"messages":[{"role":"user","content":"Count from 1 to 5, separated by spaces."}]}`, nil)
	if r.status != 200 || !strings.Contains(r.body, "chat.completion.chunk") || !strings.HasSuffix(r.body, "data: [DONE]\n\n") {
		t.Fatalf("a streamed chat: %d %s", r.status, r.body)
	}
	sums, _ := s.Usage(t.Context(), time.Time{})
	t.Logf("usage: %+v", sums)
	after, _, _ := s.login(t.Context(), "plan")
	if after.RefreshToken != l.RefreshToken {
		t.Error("the live test refreshed the login")
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
