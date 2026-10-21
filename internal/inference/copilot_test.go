package inference

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frostyard/lodge/internal/copilotauth"
	"github.com/frostyard/lodge/internal/store"
)

// copilotFake is a fake of GitHub's token exchange and the Copilot API.
type copilotFake struct {
	srv *httptest.Server

	mu        sync.Mutex
	github    string // the GitHub token the exchange takes
	exchanges int
	refuse    int // the exchange's status when not 0
	expiresIn time.Duration
	now       func() time.Time
	reqs      []seenReq
	reject401 int // the API answers 401 to this many requests first
}

func newCopilotFake(t *testing.T) *copilotFake {
	f := &copilotFake{github: "gho_owner", expiresIn: 30 * time.Minute}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.refuse != 0 {
			w.WriteHeader(f.refuse)
			return
		}
		if r.Header.Get("Authorization") != "token "+f.github || r.Header.Get("Copilot-Integration-Id") != copilotauth.IntegrationID {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.exchanges++
		_ = json.MarshalWrite(w, map[string]any{"token": fmt.Sprintf("cop-%d", f.exchanges), "sku": "monthly_subscriber",
			"expires_at": f.now().Add(f.expiresIn).Unix(), "refresh_in": int64(f.expiresIn/time.Second) - 300,
			"endpoints": map[string]any{"api": "https://api.individual.githubcopilot.com"}})
	})
	mux.HandleFunc("GET /models", func(w http.ResponseWriter, r *http.Request) {
		if !f.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"data":[{"id":"gpt-test","name":"GPT Test"},{"id":"claude-test"}]}`)
	})
	api := func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.UnmarshalRead(r.Body, &body)
		f.mu.Lock()
		f.reqs = append(f.reqs, seenReq{r.URL.Path, r.Header.Clone(), body})
		reject := f.reject401 > 0
		if reject {
			f.reject401--
		}
		f.mu.Unlock()
		if reject || !f.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
			io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},`+
			`"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`)
	}
	mux.HandleFunc("POST /chat/completions", api)
	mux.HandleFunc("POST /responses", api)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// authorized takes the token of the latest exchange only.
func (f *copilotFake) authorized(r *http.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return r.Header.Get("Authorization") == fmt.Sprintf("Bearer cop-%d", f.exchanges)
}

func (f *copilotFake) last(t *testing.T) seenReq {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("the Copilot API received nothing")
	}
	return f.reqs[len(f.reqs)-1]
}

func (f *copilotFake) exchanged() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchanges
}

type copilotHarness struct {
	*harness
	fake *copilotFake
	mu   sync.Mutex
	now  time.Time
}

func (h *copilotHarness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

func (h *copilotHarness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func newCopilotHarness(t *testing.T, key string) *copilotHarness {
	t.Helper()
	f := newCopilotFake(t)
	h := &copilotHarness{fake: f, now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	f.now = h.clock
	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logs := &syncBuf{}
	s := New(Options{DB: db, Callers: fakeCallers{os.Getuid(): "porter"}, DevUID: -1, Log: slog.New(slog.NewTextHandler(logs, nil)),
		Now: h.clock, Copilot: copilotauth.Endpoints{Exchange: f.srv.URL + "/copilot_internal/v2/token"}})
	// The configuration names the fake's address, as a test or another
	// GitHub would; the exchange's endpoint is then not used.
	s.Load(Config{
		Providers: map[string]ProviderConfig{"copilot": {Type: TypeCopilot, BaseURL: f.srv.URL, Slots: 2}},
		Aliases:   map[string]AliasConfig{"smart": {Provider: "copilot", Model: "gpt-test"}},
	}, nil)
	if key != "" {
		if err := s.SetKey(t.Context(), "copilot", key); err != nil {
			t.Fatal(err)
		}
	}
	s.CheckAll(t.Context())
	sock := filepath.Join(dir, "inference.sock")
	ln, err := Listen(sock, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	h.harness = &harness{t: t, s: s, client: client, logs: logs}
	return h
}

func (h *copilotHarness) status() ProviderStatus {
	h.t.Helper()
	st, _ := h.s.Status()
	if len(st) != 1 {
		h.t.Fatalf("status = %+v", st)
	}
	return st[0]
}

const copilotChat = `{"model":"smart","messages":[{"role":"system","content":"You are Porter."},{"role":"user","content":"` + secretPrompt + `"}],` +
	`"tools":[{"type":"function","function":{"name":"notes__read_note","parameters":{"type":"object"}}}]}`

func TestCopilotChatForwards(t *testing.T) {
	h := newCopilotHarness(t, "gho_owner")
	if st := h.status(); !st.Available || st.Login != "GitHub Copilot (monthly_subscriber)" {
		t.Fatalf("status = %+v", st)
	}
	r := h.do(http.MethodPost, "/v1/chat/completions", copilotChat, map[string]string{"Authorization": "Bearer app-secret"})
	if r.status != 200 || !strings.Contains(r.body, `"content":"Hi"`) {
		t.Fatalf("chat = %d %q", r.status, r.body)
	}
	got := h.fake.last(t)
	if got.Path != "/chat/completions" || got.Body["model"] != "gpt-test" || got.Body["tools"] == nil {
		t.Errorf("the API got %s %v", got.Path, got.Body)
	}
	for k, v := range map[string]string{
		"Authorization":          "Bearer cop-1",
		"Copilot-Integration-Id": copilotauth.IntegrationID,
		"Editor-Version":         copilotauth.EditorVersion,
		"Editor-Plugin-Version":  copilotauth.EditorPluginVersion,
		"User-Agent":             copilotauth.UserAgent,
		"Openai-Intent":          "conversation-edits",
		"X-Initiator":            "user",
	} {
		if got.Header.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, got.Header.Get(k), v)
		}
	}
	// The usage is logged, and the prompt is not.
	sums, err := h.s.Usage(t.Context(), time.Time{})
	if err != nil || len(sums) != 1 || sums[0].Input != 7 || sums[0].Output != 2 {
		t.Errorf("usage = %+v %v", sums, err)
	}
	if strings.Contains(h.logs.String(), secretPrompt) {
		t.Error("the log holds the prompt")
	}
	// A turn after a tool result is the agent's.
	body := `{"model":"smart","messages":[{"role":"user","content":"read a.md"},{"role":"assistant","content":null,` +
		`"tool_calls":[{"id":"c1","type":"function","function":{"name":"notes__read_note","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"x"}]}`
	h.do(http.MethodPost, "/v1/chat/completions", body, nil)
	if got := h.fake.last(t).Header.Get("X-Initiator"); got != "agent" {
		t.Errorf("X-Initiator after a tool result = %q", got)
	}
	// A Responses request goes to /responses as it is.
	h.do(http.MethodPost, "/v1/responses", `{"model":"smart","input":"hi"}`, nil)
	if got := h.fake.last(t); got.Path != "/responses" || got.Body["model"] != "gpt-test" || got.Header.Get("X-Initiator") != "user" {
		t.Errorf("responses: %s %v %q", got.Path, got.Body, got.Header.Get("X-Initiator"))
	}
	if n := h.fake.exchanged(); n != 1 {
		t.Errorf("%d exchanges, want 1: the token is kept until it is due", n)
	}
}

func TestCopilotStreams(t *testing.T) {
	h := newCopilotHarness(t, "gho_owner")
	r := h.do(http.MethodPost, "/v1/chat/completions", strings.Replace(copilotChat, `"model":"smart"`, `"model":"smart","stream":true`, 1),
		map[string]string{"Accept": "text/event-stream"})
	if r.status != 200 || !strings.Contains(r.body, "[DONE]") || r.header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream = %d %q", r.status, r.body)
	}
	sums, _ := h.s.Usage(t.Context(), time.Time{})
	if len(sums) != 1 || sums[0].Input != 7 || sums[0].Output != 2 {
		t.Errorf("usage = %+v", sums)
	}
}

// The Copilot token is exchanged again when it is due, after a 401, and
// when the GitHub token changes.
func TestCopilotRenews(t *testing.T) {
	h := newCopilotHarness(t, "gho_owner")
	h.advance(26 * time.Minute) // past refresh_in (25 minutes)
	if r := h.do(http.MethodPost, "/v1/chat/completions", copilotChat, nil); r.status != 200 {
		t.Fatalf("after refresh_in: %d %q", r.status, r.body)
	}
	if n := h.fake.exchanged(); n != 2 {
		t.Errorf("%d exchanges after refresh_in, want 2", n)
	}
	h.fake.mu.Lock()
	h.fake.reject401 = 1
	h.fake.mu.Unlock()
	if r := h.do(http.MethodPost, "/v1/chat/completions", copilotChat, nil); r.status != 200 {
		t.Fatalf("after a 401: %d %q", r.status, r.body)
	}
	if n := h.fake.exchanged(); n != 3 {
		t.Errorf("%d exchanges after a 401, want 3", n)
	}
	h.fake.mu.Lock()
	h.fake.github = "gho_new"
	h.fake.mu.Unlock()
	if err := h.s.SetKey(t.Context(), "copilot", "gho_new"); err != nil {
		t.Fatal(err)
	}
	if r := h.do(http.MethodPost, "/v1/chat/completions", copilotChat, nil); r.status != 200 {
		t.Fatalf("after a new GitHub token: %d %q", r.status, r.body)
	}
	if n := h.fake.exchanged(); n != 4 {
		t.Errorf("%d exchanges after a new GitHub token, want 4", n)
	}
}

func TestCopilotSignInRefusals(t *testing.T) {
	h := newCopilotHarness(t, "")
	if st := h.status(); st.Available || !strings.Contains(st.Reason, "not signed in: run lodgectl provider login copilot --copilot") {
		t.Errorf("without a key: %+v", st)
	}
	r := h.do(http.MethodPost, "/v1/chat/completions", copilotChat, nil)
	if r.status != 503 || errorCode(t, r) != "provider_unavailable" || !strings.Contains(r.body, "not signed in") {
		t.Errorf("a request without a key: %d %q", r.status, r.body)
	}
	for status, want := range map[int]string{401: "sign in again with lodgectl provider login copilot --copilot", 403: "no Copilot access"} {
		h.fake.mu.Lock()
		h.fake.refuse = status
		h.fake.mu.Unlock()
		if err := h.s.SetKey(t.Context(), "copilot", "gho_owner"); err != nil {
			t.Fatal(err)
		}
		h.s.CheckAll(t.Context())
		if st := h.status(); st.Available || !strings.Contains(st.Reason, want) {
			t.Errorf("an exchange refused with %d: %+v", status, st)
		}
	}
	// A refusal mid-use makes the provider unavailable at once.
	h.fake.mu.Lock()
	h.fake.refuse = 0
	h.fake.mu.Unlock()
	h.s.CheckAll(t.Context())
	if !h.status().Available {
		t.Fatalf("not available again: %+v", h.status())
	}
	h.fake.mu.Lock()
	h.fake.refuse = 401
	h.fake.reject401 = 1
	h.fake.mu.Unlock()
	r = h.do(http.MethodPost, "/v1/chat/completions", copilotChat, nil)
	if r.status != 503 || !strings.Contains(r.body, "sign in again") || h.status().Available {
		t.Errorf("a refusal mid-use: %d %q %+v", r.status, r.body, h.status())
	}
}

func TestCopilotModels(t *testing.T) {
	h := newCopilotHarness(t, "gho_owner")
	ms, err := h.s.Models(t.Context(), "copilot")
	if err != nil || len(ms) != 2 || ms[0] != (Model{ID: "gpt-test", Name: "GPT Test"}) || ms[1].ID != "claude-test" {
		t.Errorf("models = %+v %v", ms, err)
	}
}
