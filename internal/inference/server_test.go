package inference

import (
	"bufio"
	"bytes"
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

	"github.com/frostyard/lodge/internal/store"
)

type fakeCallers map[int]string

func (f fakeCallers) AppForUID(uid int) (string, bool) { a, ok := f[uid]; return a, ok }
func (f fakeCallers) AppEnabled(id string) bool        { return id == "mail" || id == "notes" }

type seenReq struct {
	Path   string
	Header http.Header
	Body   map[string]any
}

// provider is a fake OpenAI-compatible server.
type provider struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []seenReq
	// gate, when set, holds every completion until it is closed, after
	// the first chunk has been sent.
	gate    chan struct{}
	started chan string // the priority of each request it starts
}

func (p *provider) last(t *testing.T) seenReq {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) == 0 {
		t.Fatal("the provider received nothing")
	}
	return p.reqs[len(p.reqs)-1]
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	p := &provider{started: make(chan string, 64)}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"object":"list","data":[]}`)
			return
		}
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		p.mu.Lock()
		p.reqs = append(p.reqs, seenReq{r.URL.Path, r.Header.Clone(), body})
		gate := p.gate
		p.mu.Unlock()
		tag, _ := body["tag"].(string)
		p.started <- tag
		stream, _ := body["stream"].(bool)
		fl := w.(http.Flusher)
		switch {
		case r.URL.Path == "/v1/chat/completions" && stream:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, `data: {"choices":[{"delta":{"content":"Hel"}}]}`+"\n\n")
			fl.Flush()
			if gate != nil {
				<-gate
			}
			io.WriteString(w, `data: {"choices":[{"delta":{"content":"lo"}}]}`+"\n\n")
			io.WriteString(w, `data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7}}`+"\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
		case r.URL.Path == "/v1/chat/completions":
			if gate != nil {
				<-gate
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"choices":[{"message":{"content":"Hello"}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`)
		case r.URL.Path == "/v1/responses" && stream:
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}\n\n")
			io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":3}}}\n\n")
		case r.URL.Path == "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"output":[],"usage":{"input_tokens":5,"output_tokens":3}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type harness struct {
	t      *testing.T
	s      *Server
	p      *provider
	client *http.Client
	logs   *syncBuf
}

func configFor(p *provider, slots int) Config {
	return Config{
		Providers: map[string]ProviderConfig{
			"home":    {Type: TypeOpenAI, BaseURL: p.srv.URL + "/v1", Slots: slots, Home: true},
			"down":    {Type: TypeOpenAI, BaseURL: "http://127.0.0.1:1/v1", Slots: 1},
			"copilot": {Type: TypeCopilot},
		},
		Aliases: map[string]AliasConfig{
			"local": {Provider: "home", Model: "real-model"},
			"fast":  {Provider: "down", Model: "m"},
			"smart": {Provider: "copilot", Model: "gpt"},
		},
		Apps: map[string][]string{"notes": {"local"}},
	}
}

func newHarness(t *testing.T, slots int, opts ...func(*Options)) *harness {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(t.Context(), filepath.Join(dir, "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logs := &syncBuf{}
	o := Options{DB: db, Callers: fakeCallers{os.Getuid(): "mail"}, DevUID: -1, Log: slog.New(slog.NewTextHandler(logs, nil))}
	for _, f := range opts {
		f(&o)
	}
	s := New(o)
	p := newProvider(t)
	s.Load(configFor(p, slots), nil)
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
	return &harness{t: t, s: s, p: p, client: client, logs: logs}
}

type reply struct {
	status int
	header http.Header
	body   string
}

func (h *harness) do(method, path string, body string, hdr map[string]string) reply {
	h.t.Helper()
	req, err := http.NewRequestWithContext(h.t.Context(), method, "http://lodge"+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, resp.Header, string(b)}
}

func errorCode(t *testing.T, r reply) string {
	t.Helper()
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.body), &e); err != nil {
		t.Fatalf("not an OpenAI error: %d %q", r.status, r.body)
	}
	return e.Error.Code
}

const secretPrompt = "the secret plans for the lodge"

func chat(stream bool) string {
	return fmt.Sprintf(`{"model":"local","stream":%v,"messages":[{"role":"user","content":%q}],"chat_template_kwargs":{"enable_thinking":false}}`,
		stream, secretPrompt)
}

func TestModels(t *testing.T) {
	h := newHarness(t, 4)
	r := h.do(http.MethodGet, "/v1/models", "", nil)
	var got struct {
		Object string       `json:"object"`
		Data   []modelEntry `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.body), &got); err != nil || r.status != 200 || got.Object != "list" {
		t.Fatalf("models: %d %q", r.status, r.body)
	}
	want := []modelEntry{
		{ID: "local", Object: "model", OwnedBy: "lodge", Available: true},
		{ID: "fast", Object: "model", OwnedBy: "lodge", Available: false},
		{ID: "smart", Object: "model", OwnedBy: "lodge", Available: false},
	}
	if fmt.Sprint(got.Data) != fmt.Sprint(want) {
		t.Errorf("models = %+v, want %+v", got.Data, want)
	}
	if r := h.do(http.MethodPost, "/v1/models", "", nil); r.status != http.StatusMethodNotAllowed {
		t.Errorf("POST /v1/models: %d", r.status)
	}
}

func TestChatForwardsAndLogsUsage(t *testing.T) {
	h := newHarness(t, 4)
	if err := h.s.SetKey(t.Context(), "home", "sk-provider"); err != nil {
		t.Fatal(err)
	}
	r := h.do(http.MethodPost, "/v1/chat/completions", chat(false), map[string]string{
		"Authorization": "Bearer sk-app", "Lodge-Caller": "agent", "Lodge-App": "notes", "Lodge-Priority": "interactive", "Cookie": "a=b",
	})
	if r.status != 200 || !strings.Contains(r.body, "Hello") {
		t.Fatalf("chat: %d %q", r.status, r.body)
	}
	s := h.p.last(t)
	if s.Body["model"] != "real-model" || s.Path != "/v1/chat/completions" {
		t.Errorf("the provider saw model %v at %s", s.Body["model"], s.Path)
	}
	if kw, _ := s.Body["chat_template_kwargs"].(map[string]any); kw["enable_thinking"] != false {
		t.Errorf("a provider parameter was not passed through: %v", s.Body)
	}
	if got := s.Header.Get("Authorization"); got != "Bearer sk-provider" {
		t.Errorf("the provider saw Authorization %q", got)
	}
	for k := range s.Header {
		if strings.HasPrefix(k, "Lodge-") || k == "Cookie" {
			t.Errorf("the provider saw %s", k)
		}
	}
	sums, err := h.s.Usage(t.Context(), time.Time{})
	if err != nil || len(sums) != 1 || sums[0] != (Sum{App: "mail", Alias: "local", Requests: 1, Input: 11, Output: 7}) {
		t.Errorf("usage = %+v, %v", sums, err)
	}
	logs := h.logs.String()
	for _, secret := range []string{secretPrompt, "Hello", "sk-provider", "sk-app"} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log holds %q:\n%s", secret, logs)
		}
	}
	for _, want := range []string{"app=mail", "alias=local", "provider=home", "model=real-model", "priority=interactive", "input_tokens=11", "output_tokens=7", "status=200"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the log lacks %s:\n%s", want, logs)
		}
	}
	var stored string
	h.s.db.QueryRow(`SELECT group_concat(app || alias || model, ',') FROM usage`).Scan(&stored)
	if strings.Contains(stored, secretPrompt) {
		t.Error("the database holds the prompt")
	}
}

// A stream reaches the app as it comes, and its last chunk's usage is
// logged.
func TestChatStreams(t *testing.T) {
	h := newHarness(t, 4)
	gate := make(chan struct{})
	h.p.mu.Lock()
	h.p.gate = gate
	h.p.mu.Unlock()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://lodge/v1/chat/completions", strings.NewReader(chat(true)))
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}
	rd := bufio.NewReader(resp.Body)
	line, err := rd.ReadString('\n')
	if err != nil || !strings.Contains(line, "Hel") {
		t.Fatalf("the first chunk did not arrive while the provider held the rest: %q %v", line, err)
	}
	close(gate)
	rest, _ := io.ReadAll(rd)
	if !strings.Contains(string(rest), "[DONE]") {
		t.Errorf("the rest: %q", rest)
	}
	sums, _ := h.s.Usage(t.Context(), time.Time{})
	if len(sums) != 1 || sums[0].Input != 11 || sums[0].Output != 7 {
		t.Errorf("usage = %+v", sums)
	}
}

func TestResponses(t *testing.T) {
	h := newHarness(t, 4)
	for _, stream := range []bool{false, true} {
		r := h.do(http.MethodPost, "/v1/responses", fmt.Sprintf(`{"model":"local","stream":%v,"input":"hi"}`, stream), nil)
		if r.status != 200 {
			t.Fatalf("responses (stream %v): %d %q", stream, r.status, r.body)
		}
		if h.p.last(t).Path != "/v1/responses" {
			t.Errorf("the provider saw %s", h.p.last(t).Path)
		}
	}
	sums, _ := h.s.Usage(t.Context(), time.Time{})
	if len(sums) != 1 || sums[0].Requests != 2 || sums[0].Input != 10 || sums[0].Output != 6 {
		t.Errorf("usage = %+v", sums)
	}
}

func TestRefusals(t *testing.T) {
	h := newHarness(t, 4)
	for name, c := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"unknown alias":   {`{"model":"huge"}`, 404, "model_not_found"},
		"a provider name": {`{"model":"real-model"}`, 404, "model_not_found"},
		"not an object":   {`[1]`, 400, "invalid_body"},
		"no model":        {`{"messages":[]}`, 400, "invalid_body"},
		"numeric model":   {`{"model":1}`, 400, "invalid_body"},
		"unavailable":     {`{"model":"fast"}`, 503, "provider_unavailable"},
		"not built":       {`{"model":"smart"}`, 503, "provider_unavailable"},
	} {
		r := h.do(http.MethodPost, "/v1/chat/completions", c.body, nil)
		if r.status != c.status || errorCode(t, r) != c.code {
			t.Errorf("%s: %d %q, want %d %s", name, r.status, r.body, c.status, c.code)
		}
	}
	if r := h.do(http.MethodPost, "/v1/embeddings", `{"model":"local"}`, nil); r.status != 404 {
		t.Errorf("an unknown endpoint: %d", r.status)
	}
	if r := h.do(http.MethodGet, "/v1/chat/completions", "", nil); r.status != http.StatusMethodNotAllowed {
		t.Errorf("GET chat: %d", r.status)
	}
	// 32 MiB is the limit.
	big := `{"model":"local","pad":"` + strings.Repeat("a", MaxBody) + `"}`
	if r := h.do(http.MethodPost, "/v1/chat/completions", big, nil); r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("past 32 MiB: %d", r.status)
	}
	at := `{"model":"local","pad":"` + strings.Repeat("a", MaxBody-len(`{"model":"local","pad":""}`)) + `"}`
	if r := h.do(http.MethodPost, "/v1/chat/completions", at, nil); r.status != 200 {
		t.Errorf("at 32 MiB: %d", r.status)
	}
	// Nothing refused reached the provider but the two good requests.
	h.p.mu.Lock()
	n := len(h.p.reqs)
	h.p.mu.Unlock()
	if n != 1 {
		t.Errorf("the provider received %d requests, want 1", n)
	}
}

// An app limited to some aliases sees and uses only those.
func TestPerAppAllowList(t *testing.T) {
	h := newHarness(t, 4, func(o *Options) { o.Callers = fakeCallers{os.Getuid(): "notes"} })
	r := h.do(http.MethodGet, "/v1/models", "", nil)
	if !strings.Contains(r.body, `"local"`) || strings.Contains(r.body, `"fast"`) {
		t.Errorf("notes' models: %q", r.body)
	}
	if r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"fast"}`, nil); r.status != 404 || errorCode(t, r) != "model_not_found" {
		t.Errorf("an alias notes may not use: %d %q", r.status, r.body)
	}
}

// The caller is the peer's uid: no header names another app.
func TestCaller(t *testing.T) {
	h := newHarness(t, 4, func(o *Options) { o.Callers = fakeCallers{} })
	for _, hdr := range []map[string]string{nil, {"Lodge-App": "mail"}} {
		r := h.do(http.MethodGet, "/v1/models", "", hdr)
		if r.status != 403 || errorCode(t, r) != "unknown_caller" {
			t.Errorf("an unknown peer with %v: %d %q", hdr, r.status, r.body)
		}
	}
	// Under a layout root, lodge's own uid names its app in Lodge-App.
	dev := newHarness(t, 4, func(o *Options) { o.Callers = fakeCallers{}; o.DevUID = os.Getuid() })
	if r := dev.do(http.MethodGet, "/v1/models", "", map[string]string{"Lodge-App": "mail"}); r.status != 200 {
		t.Errorf("Lodge-App under a layout root: %d %q", r.status, r.body)
	}
	for _, app := range []string{"", "nosuch"} {
		if r := dev.do(http.MethodGet, "/v1/models", "", map[string]string{"Lodge-App": app}); r.status != 403 {
			t.Errorf("Lodge-App %q under a layout root: %d", app, r.status)
		}
	}
}

// While background requests hold every slot they may, an interactive
// request starts at once (roadmap Phase 3, Done when).
func TestInteractiveStartsWhileBackgroundHoldsItsShare(t *testing.T) {
	h := newHarness(t, 2)
	gate := make(chan struct{})
	h.p.mu.Lock()
	h.p.gate = gate
	h.p.mu.Unlock()
	body := func(tag string) string { return `{"model":"local","tag":"` + tag + `"}` }
	var wg sync.WaitGroup
	statuses := make(chan int, 3)
	send := func(tag, priority string) {
		wg.Go(func() {
			req, _ := http.NewRequest(http.MethodPost, "http://lodge/v1/chat/completions", strings.NewReader(body(tag)))
			req.Header.Set("Lodge-Priority", priority)
			resp, err := h.client.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			statuses <- resp.StatusCode
		})
	}
	started := func() string {
		select {
		case tag := <-h.p.started:
			return tag
		case <-time.After(10 * time.Second):
			t.Fatal("nothing started")
			return ""
		}
	}
	send("bg1", "background")
	if tag := started(); tag != "bg1" {
		t.Fatalf("started %s", tag)
	}
	send("bg2", "background")
	// bg2 waits: background holds slots − 1 = 1.
	home := h.s.state().providers["home"]
	waitLoad(t, home.Queue, Load{Running: 1, Waiting: 1})
	send("int", "interactive")
	if tag := started(); tag != "int" {
		t.Fatalf("started %s before the interactive request", tag)
	}
	close(gate)
	if tag := started(); tag != "bg2" {
		t.Fatalf("then started %s", tag)
	}
	wg.Wait()
	close(statuses)
	for s := range statuses {
		if s != 200 {
			t.Errorf("a request answered %d", s)
		}
	}
	sums, _ := h.s.Usage(t.Context(), time.Time{})
	if len(sums) != 1 || sums[0].Requests != 3 {
		t.Errorf("usage = %+v", sums)
	}
}

func waitLoad(t *testing.T, q *Queue, want Load) {
	t.Helper()
	q.mu.Lock()
	if q.changed == nil {
		q.changed = make(chan struct{}, 2*MaxWaiting)
	}
	q.mu.Unlock()
	for q.Load() != want {
		select {
		case <-q.changed:
		case <-time.After(10 * time.Second):
			t.Fatalf("load %+v, want %+v", q.Load(), want)
		}
	}
}

// A provider that cannot be reached is unavailable at once, and found
// again when it answers.
func TestAvailability(t *testing.T) {
	h := newHarness(t, 4)
	st, refused := h.s.Status()
	if refused != "" || len(st) != 3 {
		t.Fatalf("status = %+v %q", st, refused)
	}
	byName := map[string]ProviderStatus{}
	for _, p := range st {
		byName[p.Name] = p
	}
	if !byName["home"].Available || byName["down"].Available || byName["down"].Reason != "cannot be reached" ||
		!strings.Contains(byName["copilot"].Reason, "not built") {
		t.Errorf("status = %+v", byName)
	}
	start := time.Now()
	r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"fast"}`, nil)
	if r.status != 503 || time.Since(start) > time.Second {
		t.Errorf("an unavailable alias: %d after %v", r.status, time.Since(start))
	}
	// The provider stops answering: the next request fails (502) and marks
	// it unavailable; the one after fails at once.
	h.p.srv.Close()
	if r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"local"}`, nil); r.status != 502 || errorCode(t, r) != "provider_failed" {
		t.Errorf("a provider that went away: %d %q", r.status, r.body)
	}
	if r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"local"}`, nil); r.status != 503 {
		t.Errorf("after a failure: %d", r.status)
	}
}

func TestRefusedConfiguration(t *testing.T) {
	h := newHarness(t, 4)
	h.s.Load(Config{}, fmt.Errorf("alias local must map to a provider on the owner's own hardware"))
	if _, refused := h.s.Status(); !strings.Contains(refused, "own hardware") {
		t.Errorf("refused = %q", refused)
	}
	if r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"local"}`, nil); r.status != 404 {
		t.Errorf("with a refused configuration: %d", r.status)
	}
}

func TestSetKey(t *testing.T) {
	h := newHarness(t, 4)
	if err := h.s.SetKey(t.Context(), "nosuch", "k"); err != ErrNoProvider {
		t.Errorf("an unknown provider: %v", err)
	}
	h.s.SetKey(t.Context(), "home", "sk-1")
	h.s.SetKey(t.Context(), "home", "")
	h.do(http.MethodPost, "/v1/chat/completions", `{"model":"local"}`, nil)
	if got := h.p.last(t).Header.Get("Authorization"); got != "" {
		t.Errorf("a removed key was sent: %q", got)
	}
}

func TestListenMode(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "run/lodge/inference.sock")
	ln, err := Listen(sock, "")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o660 || fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("the socket: %v %v", fi.Mode(), err)
	}
	if _, err := Listen(filepath.Join(t.TempDir(), "x.sock"), "nosuchlodgegroup"); err == nil {
		t.Error("an unknown group was accepted")
	}
}

func TestTapUsage(t *testing.T) {
	for name, c := range map[string]struct {
		stream bool
		chunks []string
		in     int64
		out    int64
	}{
		"chat":          {false, []string{`{"usage":{"prompt_tokens":3,`, `"completion_tokens":4}}`}, 3, 4},
		"responses":     {false, []string{`{"usage":{"input_tokens":5,"output_tokens":6}}`}, 5, 6},
		"none":          {false, []string{`{"choices":[]}`}, -1, -1},
		"chat stream":   {true, []string{"data: {\"choices\":[]}\n\nda", "ta: {\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":8}}\n\ndata: [DONE]\n\n"}, 7, 8},
		"events":        {true, []string{"event: response.completed\r\ndata: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\r\n\r\n"}, 1, 2},
		"stream, none":  {true, []string{"data: {\"choices\":[]}\n\n"}, -1, -1},
		"not json data": {true, []string{"data: hello\n\n"}, -1, -1},
	} {
		tap := newTap(c.stream)
		for _, ch := range c.chunks {
			tap.Write([]byte(ch))
		}
		tap.Finish()
		if tap.in != c.in || tap.out != c.out {
			t.Errorf("%s: %d, %d; want %d, %d", name, tap.in, tap.out, c.in, c.out)
		}
	}
}

// A provider not checked yet, after start or a reload that changed it, is
// waited for, not refused.
func TestUncheckedProviderIsWaitedFor(t *testing.T) {
	h := newHarness(t, 4)
	h.s.Load(configFor(h.p, 3), nil) // changed slots: a new, unchecked provider
	if st, _ := h.s.Status(); st[2].Name != "home" || st[2].Reason != "not checked yet" {
		t.Fatalf("status = %+v", st)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go h.s.Run(ctx)
	if r := h.do(http.MethodPost, "/v1/chat/completions", `{"model":"local"}`, nil); r.status != 200 {
		t.Errorf("right after a reload: %d %q", r.status, r.body)
	}
}
