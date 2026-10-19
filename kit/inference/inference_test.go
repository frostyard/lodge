package inference_test

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/frostyard/lodge/kit/inference"
	"github.com/frostyard/lodge/kit/lodgeapp"
)

// seen is what the fake socket received.
type seen struct {
	method, path      string
	priority, app     string
	auth, contentType string
	body              map[string]jsontext.Value
}

// fake serves a fake inference socket under a temp root, as lodge would,
// and records each request.
type fake struct {
	env lodgeapp.Env

	mu   sync.Mutex
	reqs []seen
	// hold, when set, is closed by the handler of a stream it holds open.
	hold chan struct{}
}

func newFake(t *testing.T) *fake {
	t.Helper()
	root, err := os.MkdirTemp("", "inf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	f := &fake{env: lodgeapp.Env{Root: root, LodgeUID: os.Getuid()}}
	path := f.env.Path(lodgeapp.InferenceSocket)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f
}

func (f *fake) last() seen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func oops(w http.ResponseWriter, status int, typ, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":{"message":%q,"type":%q,"code":%q}}`, msg, typ, code)
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s := seen{method: r.Method, path: r.URL.Path, priority: r.Header.Get(inference.HeaderPriority),
		app: r.Header.Get(inference.HeaderApp), auth: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type")}
	if r.Method == http.MethodPost {
		_ = json.UnmarshalRead(r.Body, &s.body)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, s)
	f.mu.Unlock()
	var model string
	_ = json.Unmarshal(s.body["model"], &model)
	stream := string(s.body["stream"]) == "true"
	switch {
	case r.URL.Path == "/v1/models":
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"local","object":"model","owned_by":"lodge","available":true},`+
			`{"id":"smart","object":"model","owned_by":"lodge","available":false}]}`)
	case model == "nope":
		oops(w, http.StatusNotFound, "invalid_request_error", "model_not_found", "no alias nope for mail")
	case model == "down":
		oops(w, http.StatusServiceUnavailable, "server_error", "provider_unavailable", "home is unreachable")
	case model == "plain":
		http.Error(w, "the provider fell over", http.StatusBadGateway)
	case r.URL.Path == "/v1/chat/completions" && !stream:
		w.Header().Set("Content-Type", "application/json")
		content, _ := json.Marshal("thinking=" + string(s.body["chat_template_kwargs"]))
		_, _ = fmt.Fprintf(w, `{"id":"c1","model":"halogen","choices":[{"index":0,"message":{"role":"assistant",`+
			`"content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2},"system_fingerprint":"x"}`,
			content)
	case r.URL.Path == "/v1/chat/completions":
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, part := range []string{"Hel", "lo"} {
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", part)
			fl.Flush()
		}
		if f.hold != nil {
			<-r.Context().Done()
			close(f.hold)
			return
		}
		_, _ = io.WriteString(w, ": keepalive\n\ndata: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	case r.URL.Path == "/v1/responses" && !stream:
		_, _ = io.WriteString(w, `{"id":"r1","output_text":"hi","usage":{"input_tokens":4,"output_tokens":1}}`)
	case r.URL.Path == "/v1/responses":
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"delta\":\"h\"}\n\n"+
			"event: response.output_text.delta\ndata: {\"delta\":\"i\"}\n\n"+
			"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":4,\"output_tokens\":1}}}\n\n")
	default:
		http.NotFound(w, r)
	}
}

func TestModelsAndHeaders(t *testing.T) {
	f := newFake(t)
	c := inference.New(f.env, "mail")
	defer c.Close()
	models, err := c.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []inference.Model{{ID: "local", Object: "model", OwnedBy: "lodge", Available: true},
		{ID: "smart", Object: "model", OwnedBy: "lodge"}}
	if fmt.Sprint(models) != fmt.Sprint(want) {
		t.Errorf("Models = %+v", models)
	}
	if s := f.last(); s.method != http.MethodGet || s.app != "mail" || s.priority != "" || s.auth != "" {
		t.Errorf("GET /v1/models sent %+v; want Lodge-App under the root, and no priority or key", s)
	}
	// Outside a root, no header names the app: lodge knows it by its uid.
	prod := inference.Dial(f.env.Path(lodgeapp.InferenceSocket), "")
	defer prod.Close()
	if _, err := prod.Models(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s := f.last(); s.app != "" {
		t.Errorf("a client outside the root sent Lodge-App %q", s.app)
	}
	if got := inference.New(lodgeapp.Env{LodgeUID: 1}, "mail"); got == nil {
		t.Error("New outside a root")
	}
}

func TestChat(t *testing.T) {
	f := newFake(t)
	c := inference.New(f.env, "mail")
	defer c.Close()
	var out inference.ChatCompletion
	err := c.Chat(t.Context(), inference.Interactive, inference.ChatRequest{
		Model:    "local",
		Messages: []inference.Message{{Role: "user", Content: "hi"}},
		Extra:    inference.Extra{"chat_template_kwargs": jsontext.Value(`{"enable_thinking":false}`), "stream": jsontext.Value("true")},
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	s := f.last()
	if s.priority != "interactive" || s.contentType != "application/json" || string(s.body["stream"]) != "false" {
		t.Errorf("chat sent priority %q, type %q, stream %s", s.priority, s.contentType, s.body["stream"])
	}
	if string(s.body["messages"]) != `[{"role":"user","content":"hi"}]` {
		t.Errorf("messages = %s", s.body["messages"])
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != `thinking={"enable_thinking":false}` ||
		out.Choices[0].FinishReason != "stop" || out.Usage == nil || out.Usage.PromptTokens != 3 ||
		string(out.Extra["system_fingerprint"]) != `"x"` {
		t.Errorf("Chat = %+v; want the provider's parameter passed through, and its answer whole", out)
	}
}

func TestChatStream(t *testing.T) {
	f := newFake(t)
	c := inference.New(f.env, "mail")
	defer c.Close()
	st, err := c.ChatStream(t.Context(), inference.Background, map[string]any{"model": "local", "messages": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if s := f.last(); s.priority != "background" || string(s.body["stream"]) != "true" {
		t.Errorf("stream sent priority %q, stream %s", s.priority, s.body["stream"])
	}
	var text strings.Builder
	var usage *inference.Usage
	for {
		ev, err := st.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var ch inference.ChatChunk
		if err := ev.Decode(&ch); err != nil {
			t.Fatal(err)
		}
		for _, choice := range ch.Choices {
			text.WriteString(choice.Delta.Content)
		}
		if ch.Usage != nil {
			usage = ch.Usage
		}
	}
	if text.String() != "Hello" || usage == nil || usage.CompletionTokens != 2 {
		t.Errorf("streamed %q, usage %+v", text.String(), usage)
	}
	if _, err := st.Next(); err != io.EOF {
		t.Errorf("Next after [DONE] = %v", err)
	}
}

func TestResponses(t *testing.T) {
	f := newFake(t)
	c := inference.New(f.env, "mail")
	defer c.Close()
	var out struct {
		OutputText string          `json:"output_text"`
		Usage      inference.Usage `json:"usage"`
	}
	if err := c.Respond(t.Context(), inference.Interactive, map[string]string{"model": "fast", "input": "hi"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.OutputText != "hi" || out.Usage.InputTokens != 4 || f.last().path != "/v1/responses" {
		t.Errorf("Respond = %+v", out)
	}
	st, err := c.RespondStream(t.Context(), inference.Interactive, map[string]string{"model": "fast", "input": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var types []string
	for {
		ev, err := st.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, ev.Type)
	}
	if strings.Join(types, ",") != "response.output_text.delta,response.output_text.delta,response.completed" {
		t.Errorf("events %v", types)
	}
}

func TestErrors(t *testing.T) {
	f := newFake(t)
	c := inference.New(f.env, "mail")
	defer c.Close()
	for _, tc := range []struct {
		model  string
		status int
		code   string
	}{
		{"nope", 404, "model_not_found"},
		{"down", 503, "provider_unavailable"},
		{"plain", 502, ""},
	} {
		err := c.Chat(t.Context(), inference.Interactive, inference.ChatRequest{Model: tc.model}, nil)
		var e *inference.Error
		if !errors.As(err, &e) || e.Status != tc.status || e.Code != tc.code || e.Message == "" {
			t.Errorf("%s: %v, want %d %q", tc.model, err, tc.status, tc.code)
		}
		if _, err := c.ChatStream(t.Context(), inference.Interactive, inference.ChatRequest{Model: tc.model}); !errors.As(err, &e) {
			t.Errorf("%s streamed: %v", tc.model, err)
		}
	}
	if err := c.Chat(t.Context(), inference.Interactive, []string{"not", "an", "object"}, nil); err == nil {
		t.Error("a body that is no object was sent")
	}
	gone := inference.Dial(filepath.Join(f.env.Root, "none.sock"), "")
	if _, err := gone.Models(t.Context()); err == nil {
		t.Error("Models with no socket succeeded")
	}
}

func TestCancellingEndsAStream(t *testing.T) {
	f := newFake(t)
	f.hold = make(chan struct{})
	c := inference.New(f.env, "mail")
	defer c.Close()
	ctx, cancel := context.WithCancel(t.Context())
	st, err := c.ChatStream(ctx, inference.Interactive, inference.ChatRequest{Model: "local"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := st.Next(); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	if _, err := st.Next(); err == nil || err == io.EOF {
		t.Errorf("Next after cancel = %v, want an error", err)
	}
	<-f.hold // lodge saw the app go away
}
