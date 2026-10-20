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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/frostyard/lodge/internal/codexauth"
	"github.com/frostyard/lodge/internal/store"
)

// plan is a fake of OpenAI's plan usage: the token endpoint, which rotates
// refresh tokens, and the Responses API, which answers from a script.
type plan struct {
	srv *httptest.Server

	mu        sync.Mutex
	access    string // the access token it accepts
	refresh   string // the live refresh token
	refreshes int
	reqs      []seenReq
	// script answers POST /v1/responses: a status, and the stream's events
	// (or the body, when the status is not 200).
	script func(n int, body map[string]any) (int, []string)
	// reject401 answers 401 to this many requests first.
	reject401 int
}

func newPlan(t *testing.T) *plan {
	p := &plan{access: "at-1", refresh: "rt-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		defer p.mu.Unlock()
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != p.refresh ||
			r.PostForm.Get("client_id") != "oaiapp_1" || r.PostForm.Get("resource") != codexauth.Resource {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"refresh_token_reused"}`)
			return
		}
		p.refreshes++
		p.access, p.refresh = fmt.Sprintf("at-%d", p.refreshes+1), fmt.Sprintf("rt-%d", p.refreshes+1)
		_ = json.MarshalWrite(w, map[string]any{"access_token": p.access, "refresh_token": p.refresh, "expires_in": 3600,
			"scope": codexauth.Scope})
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		if !p.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"models":[{"slug":"gpt-test","display_name":"GPT Test","visibility":"list"},{"slug":"hidden","visibility":"hide"}]}`)
	})
	mux.HandleFunc("POST /v1/responses", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.UnmarshalRead(r.Body, &body)
		p.mu.Lock()
		p.reqs = append(p.reqs, seenReq{r.URL.Path, r.Header.Clone(), body})
		n, script := len(p.reqs), p.script
		reject := p.reject401 > 0
		if reject {
			p.reject401--
		}
		p.mu.Unlock()
		if reject || !p.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"code":"subscription_sharing_invalid_user","message":"no"}}`)
			return
		}
		status, evs := script(n, body)
		if status != http.StatusOK {
			w.WriteHeader(status)
			io.WriteString(w, strings.Join(evs, ""))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range evs {
			fmt.Fprintf(w, "event: x\ndata: %s\n\n", ev)
			w.(http.Flusher).Flush()
		}
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *plan) authorized(r *http.Request) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return r.Header.Get("Authorization") == "Bearer "+p.access
}

func (p *plan) last(t *testing.T) seenReq {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) == 0 {
		t.Fatal("the plan received nothing")
	}
	return p.reqs[len(p.reqs)-1]
}

// answer is a script that answers every request with evs.
func answer(evs ...string) func(int, map[string]any) (int, []string) {
	return func(int, map[string]any) (int, []string) { return http.StatusOK, evs }
}

// An unstored response's events, as plan usage sends them: the items
// come in output_item.done, and response.completed's output is empty.
const (
	itemText      = `{"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}}`
	completedText = `{"type":"response.completed","response":{"id":"r1","model":"gpt-test","status":"completed",` +
		`"output":[],"usage":{"input_tokens":12,"output_tokens":5}}}`
)

type codexHarness struct {
	*harness
	plan *plan
	now  time.Time
}

func newCodexHarness(t *testing.T, signIn bool) *codexHarness {
	t.Helper()
	pl := newPlan(t)
	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	logs := &syncBuf{}
	s := New(Options{DB: db, Callers: fakeCallers{os.Getuid(): "porter"}, DevUID: -1, Log: slog.New(slog.NewTextHandler(logs, nil)),
		Now: func() time.Time { return now }, Auth: codexauth.Endpoints{Issuer: pl.srv.URL, Token: pl.srv.URL + "/oauth/token"}})
	s.Load(Config{
		Providers: map[string]ProviderConfig{"plan": {Type: TypeCodex, BaseURL: pl.srv.URL + "/v1", Slots: 2}},
		Aliases:   map[string]AliasConfig{"smart": {Provider: "plan", Model: "gpt-test"}},
	}, nil)
	if signIn {
		if err := s.ImportLogin(t.Context(), "plan", testLogin(now.Add(time.Hour))); err != nil {
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
	return &codexHarness{harness: &harness{t: t, s: s, client: client, logs: logs}, plan: pl, now: now}
}

func testLogin(expires time.Time) codexauth.Login {
	return codexauth.Login{Type: codexauth.Type, ClientID: "oaiapp_1", Account: "user-1", Email: "owner@example.com",
		Scope: codexauth.Scope, AccessToken: "at-1", RefreshToken: "rt-1", ExpiresAt: expires}
}

func (h *codexHarness) usage() Sum {
	h.t.Helper()
	sums, err := h.s.Usage(h.t.Context(), time.Time{})
	if err != nil || len(sums) != 1 {
		h.t.Fatalf("usage = %+v %v", sums, err)
	}
	return sums[0]
}

func TestCodexChatCallsTools(t *testing.T) {
	h := newCodexHarness(t, true)
	h.plan.script = answer(
		`{"type":"response.created","response":{"id":"r1","model":"gpt-test"}}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"c2","name":"notes__read_note","arguments":"{\"path\":\"a.md\"}"}}`,
		`{"type":"response.completed","response":{"id":"r1","model":"gpt-test","status":"completed","output":[],`+
			`"usage":{"input_tokens":12,"output_tokens":5}}}`)
	body := `{"model":"smart","temperature":0.2,"max_tokens":100,"chat_template_kwargs":{"enable_thinking":false},"tool_choice":"required",
		"messages":[{"role":"system","content":"You are Porter."},{"role":"user","content":"read a.md"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"porter__now","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"Monday"}],
		"tools":[{"type":"function","function":{"name":"notes__read_note","description":"Read a note.","parameters":{"type":"object"}}},
		{"type":"function","function":{"name":"porter__now","description":"The time."}},
		{"type":"function","function":{"name":"plain","description":"No app."}}]}`
	r := h.do(http.MethodPost, "/v1/chat/completions", body, map[string]string{"Lodge-Priority": "interactive"})
	if r.status != 200 {
		t.Fatalf("chat: %d %s", r.status, r.body)
	}
	var got struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content   *string `json:"content"`
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
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(r.body), &got); err != nil || len(got.Choices) != 1 {
		t.Fatalf("answer %q: %v", r.body, err)
	}
	c := got.Choices[0]
	if got.Object != "chat.completion" || c.Message.Content != nil || len(c.Message.ToolCalls) != 1 || c.FinishReason != "tool_calls" ||
		c.Message.ToolCalls[0].ID != "c2" || c.Message.ToolCalls[0].Function.Name != "notes__read_note" ||
		c.Message.ToolCalls[0].Function.Arguments != `{"path":"a.md"}` || got.Usage.PromptTokens != 12 {
		t.Errorf("answer = %+v", got)
	}

	sent := h.plan.last(t)
	b := sent.Body
	if sent.Header.Get("Authorization") != "Bearer at-1" || b["store"] != false || b["stream"] != true || b["model"] != "gpt-test" ||
		b["instructions"] != "You are Porter." || b["tool_choice"] != "required" {
		t.Errorf("the request: %v %v", sent.Header, b)
	}
	for _, k := range []string{"temperature", "max_tokens", "chat_template_kwargs", "messages"} {
		if _, ok := b[k]; ok {
			t.Errorf("the request carries %s", k)
		}
	}
	tools, _ := json.Marshal(b["tools"])
	for _, want := range []string{`"type":"function"`, `"name":"notes__read_note"`, `"name":"porter__now"`, `"name":"plain"`,
		`"properties":{}`} {
		if !strings.Contains(string(tools), want) {
			t.Errorf("tools lack %s: %s", want, tools)
		}
	}
	if strings.Contains(string(tools), "namespace") {
		t.Errorf("tools are grouped in namespaces, which plan usage reserves: %s", tools)
	}
	input, _ := json.Marshal(b["input"])
	for _, want := range []string{`"role":"user"`, `"type":"function_call"`, `"call_id":"c1"`, `"name":"porter__now"`,
		`"type":"function_call_output"`, `"output":"Monday"`} {
		if !strings.Contains(string(input), want) {
			t.Errorf("input lacks %s: %s", want, input)
		}
	}
	if strings.Contains(string(input), `"role":"system"`) {
		t.Errorf("input carries a system message: %s", input)
	}
	if u := h.usage(); u.App != "porter" || u.Alias != "smart" || u.Input != 12 || u.Output != 5 || u.Failures != 0 {
		t.Errorf("usage = %+v", u)
	}
	if strings.Contains(h.logs.String(), "read a.md") {
		t.Error("the log holds the prompt")
	}
}

func TestCodexStreamsChunks(t *testing.T) {
	h := newCodexHarness(t, true)
	h.plan.script = answer(
		`{"type":"response.created","response":{"id":"r1","model":"gpt-test"}}`,
		`{"type":"response.output_text.delta","delta":"Hel"}`,
		`{"type":"response.output_text.delta","delta":"lo"}`,
		completedText)
	r := h.do(http.MethodPost, "/v1/chat/completions",
		`{"model":"smart","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`, nil)
	if r.status != 200 || r.header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %d %v", r.status, r.header)
	}
	var text strings.Builder
	var finish string
	var usage bool
	for _, line := range strings.Split(r.body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var ch struct {
			Object  string `json:"object"`
			Choices []struct {
				Delta struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens int `json:"prompt_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &ch); err != nil || ch.Object != "chat.completion.chunk" {
			t.Fatalf("chunk %q: %v", data, err)
		}
		for _, c := range ch.Choices {
			text.WriteString(c.Delta.Content)
			if c.FinishReason != nil {
				finish = *c.FinishReason
			}
		}
		usage = usage || ch.Usage != nil && ch.Usage.PromptTokens == 12
	}
	if text.String() != "Hello" || finish != "stop" || !usage || !strings.HasSuffix(r.body, "data: [DONE]\n\n") {
		t.Errorf("the stream: %q", r.body)
	}
	if u := h.usage(); u.Input != 12 || u.Output != 5 {
		t.Errorf("usage = %+v", u)
	}
}

func TestCodexRefreshesBeforeExpiry(t *testing.T) {
	h := newCodexHarness(t, false)
	if err := h.s.ImportLogin(t.Context(), "plan", testLogin(h.now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	h.s.CheckAll(t.Context()) // Run does, when an import asks
	h.plan.script = answer(itemText, completedText)
	for range 2 {
		if r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, nil); r.status != 200 ||
			!strings.Contains(r.body, "Hello") {
			t.Fatalf("chat: %d %s", r.status, r.body)
		}
	}
	if h.plan.refreshes != 1 || h.plan.last(t).Header.Get("Authorization") != "Bearer at-2" {
		t.Errorf("refreshes %d, last auth %q", h.plan.refreshes, h.plan.last(t).Header.Get("Authorization"))
	}
	l, _, _ := h.s.login(t.Context(), "plan")
	if l.RefreshToken != "rt-2" || l.AccessToken != "at-2" || !l.ExpiresAt.Equal(h.now.Add(time.Hour)) {
		t.Errorf("the saved sign-in: %+v", l)
	}
}

func TestCodexRefreshesOnceOn401(t *testing.T) {
	h := newCodexHarness(t, true)
	h.plan.script = answer(itemText, completedText)
	h.plan.reject401 = 1
	if r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, nil); r.status != 200 {
		t.Fatalf("chat: %d %s", r.status, r.body)
	}
	if h.plan.refreshes != 1 {
		t.Errorf("refreshes = %d", h.plan.refreshes)
	}
}

func TestCodexSignedOut(t *testing.T) {
	h := newCodexHarness(t, false)
	l := testLogin(h.now.Add(-time.Minute))
	l.RefreshToken = "rt-spent"
	if err := h.s.ImportLogin(t.Context(), "plan", l); err != nil {
		t.Fatal(err)
	}
	h.s.CheckAll(t.Context())
	r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, nil)
	if r.status != http.StatusServiceUnavailable || errorCode(t, r) != "provider_unavailable" {
		t.Fatalf("signed out: %d %s", r.status, r.body)
	}
	st, _ := h.s.Status()
	if st[0].Available || !strings.Contains(st[0].Reason, "signed out (refresh_token_reused)") || !strings.Contains(st[0].Reason, "--client-id oaiapp_1") {
		t.Errorf("status = %+v", st[0])
	}
	saved, ok, _ := h.s.login(t.Context(), "plan")
	if !ok || saved.SignedIn() || saved.ClientID != "oaiapp_1" || saved.Email != "owner@example.com" {
		t.Errorf("the saved sign-in: %+v", saved)
	}
}

func TestCodexNotSignedIn(t *testing.T) {
	h := newCodexHarness(t, false)
	st, _ := h.s.Status()
	if st[0].Available || !strings.Contains(st[0].Reason, "not signed in") {
		t.Errorf("status = %+v", st[0])
	}
	if r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"smart","messages":[]}`, nil); r.status != http.StatusServiceUnavailable {
		t.Errorf("chat: %d %s", r.status, r.body)
	}
}

func TestCodexPlanErrors(t *testing.T) {
	h := newCodexHarness(t, true)
	h.plan.script = answer(`{"type":"response.created","response":{"id":"r1"}}`,
		`{"type":"response.failed","response":{"status":"failed","error":{"code":"subscription_sharing_usage_limit_exceeded","message":"limit"}}}`)
	r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, nil)
	if r.status != http.StatusTooManyRequests || errorCode(t, r) != "subscription_sharing_usage_limit_exceeded" || !strings.Contains(r.body, "ChatGPT settings") {
		t.Errorf("a usage limit: %d %s", r.status, r.body)
	}
	h.plan.script = func(int, map[string]any) (int, []string) {
		return http.StatusBadRequest, []string{`{"error":{"code":"subscription_sharing_unsupported_capability","param":"tools","message":"no"}}`}
	}
	r = h.do(http.MethodPost, "/v1/chat/completions", `{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, nil)
	if r.status != http.StatusBadRequest || errorCode(t, r) != "subscription_sharing_unsupported_capability" || !strings.Contains(r.body, "tools") {
		t.Errorf("an unsupported capability: %d %s", r.status, r.body)
	}
	h.plan.script = answer(`{"type":"response.output_text.delta","delta":"Hel"}`)
	r = h.do(http.MethodPost, "/v1/chat/completions", `{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, nil)
	if r.status != http.StatusBadGateway || !strings.Contains(r.body, "before the response completed") {
		t.Errorf("a stream that ends early: %d %s", r.status, r.body)
	}
	r = h.do(http.MethodPost, "/v1/chat/completions", `{"model":"smart","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if r.status != 200 || !strings.Contains(r.body, `"code":"provider_failed"`) || strings.Contains(r.body, "[DONE]") {
		t.Errorf("a streamed answer that ends early: %d %s", r.status, r.body)
	}
	if r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"smart","messages":[{"role":"tool_result","content":"x"}]}`, nil); r.status != 400 {
		t.Errorf("an unknown role: %d %s", r.status, r.body)
	}
}

func TestCodexResponsesEndpoint(t *testing.T) {
	h := newCodexHarness(t, true)
	h.plan.script = answer(`{"type":"response.output_text.delta","delta":"Hello"}`, itemText, completedText)
	r := h.do(http.MethodPost, "/v1/responses",
		`{"model":"smart","temperature":1,"background":true,"input":[{"role":"system","content":"Be brief."},{"role":"user","content":"hi"}]}`, nil)
	if r.status != 200 || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("responses: %d %s", r.status, r.body)
	}
	var got struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Output []struct {
			Type string `json:"type"`
		} `json:"output"`
	}
	if err := json.Unmarshal([]byte(r.body), &got); err != nil || got.ID != "r1" || got.Status != "completed" ||
		len(got.Output) != 1 || got.Output[0].Type != "message" {
		t.Errorf("answer %q: the stream's items belong in output", r.body)
	}
	b := h.plan.last(t).Body
	if b["store"] != false || b["stream"] != true || b["temperature"] != nil || b["background"] != nil {
		t.Errorf("the request: %v", b)
	}
	if in, _ := json.Marshal(b["input"]); !strings.Contains(string(in), `"role":"developer"`) || strings.Contains(string(in), `"role":"system"`) {
		t.Errorf("input: %s", in)
	}
	r = h.do(http.MethodPost, "/v1/responses", `{"model":"smart","stream":true,"input":"hi"}`, nil)
	if r.status != 200 || !strings.Contains(r.body, "response.completed") {
		t.Errorf("a streamed responses request: %d %s", r.status, r.body)
	}
	sums, _ := h.s.Usage(t.Context(), time.Time{})
	if len(sums) != 1 || sums[0].Requests != 2 || sums[0].Input != 24 {
		t.Errorf("usage = %+v", sums)
	}
}

func TestCodexStatusAndModels(t *testing.T) {
	h := newCodexHarness(t, true)
	st, _ := h.s.Status()
	if !st[0].Available || st[0].Login != "owner@example.com (client oaiapp_1)" || st[0].Type != TypeCodex {
		t.Errorf("status = %+v", st[0])
	}
	models, err := h.s.Models(t.Context(), "plan")
	if err != nil || len(models) != 1 || models[0].ID != "gpt-test" || models[0].Name != "GPT Test" {
		t.Errorf("models = %+v %v", models, err)
	}
	if _, err := h.s.Models(t.Context(), "nosuch"); err != ErrNoProvider {
		t.Errorf("models of no provider: %v", err)
	}
	if err := h.s.ImportLogin(t.Context(), "nosuch", testLogin(h.now)); err != ErrNoProvider {
		t.Errorf("import to no provider: %v", err)
	}
	bad := testLogin(h.now)
	bad.Scope = "openid"
	if err := h.s.ImportLogin(t.Context(), "plan", bad); err == nil {
		t.Error("a sign-in without the plan's scope was imported")
	}
}

func TestCodexConfig(t *testing.T) {
	for name, c := range map[string]struct {
		p  ProviderConfig
		ok bool
	}{
		"default base": {ProviderConfig{Type: TypeCodex, Slots: 2}, true},
		"own base":     {ProviderConfig{Type: TypeCodex, BaseURL: "https://example.com/v1", Slots: 2}, true},
		"bad base":     {ProviderConfig{Type: TypeCodex, BaseURL: "https://example.com", Slots: 2}, false},
		"no slots":     {ProviderConfig{Type: TypeCodex}, false},
		"home":         {ProviderConfig{Type: TypeCodex, Slots: 2, Home: true}, false},
	} {
		err := Config{Providers: map[string]ProviderConfig{"plan": c.p}}.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	if baseURL(ProviderConfig{Type: TypeCodex}) != CodexBaseURL {
		t.Error("a codex provider's default base")
	}
	if u, _ := url.Parse(CodexBaseURL); u.Host != "api.openai.com" {
		t.Error("plan usage goes to the public Responses API")
	}
}

// A response.completed that carries its output, as a stored response's
// does, answers as well as the items do.
func TestCodexReadsACompletedOutput(t *testing.T) {
	h := newCodexHarness(t, true)
	h.plan.script = answer(`{"type":"response.completed","response":{"id":"r1","model":"gpt-test","status":"completed",` +
		`"output":[{"type":"message","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	for _, stream := range []bool{false, true} {
		r := h.do(http.MethodPost, "/v1/chat/completions", fmt.Sprintf(`{"model":"smart","stream":%v,"messages":[{"role":"user","content":"hi"}]}`, stream), nil)
		if r.status != 200 || !strings.Contains(r.body, "Hello") {
			t.Errorf("stream %v: %d %s", stream, r.status, r.body)
		}
	}
}
