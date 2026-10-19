// Package inference is an app daemon's client of lodge's inference socket,
// /run/lodge/inference.sock: OpenAI's chat completions and responses, and
// the aliases the app may use
// (https://github.com/frostyard/lodge/blob/main/docs/specs/app-contract.md#inference).
//
//	ai := inference.New(env, "mail") // env from lodgeapp.LoadEnv
//	models, err := ai.Models(ctx)
//	var out inference.ChatCompletion
//	err = ai.Chat(ctx, inference.Interactive, inference.ChatRequest{
//		Model:    "local",
//		Messages: []inference.Message{{Role: "user", Content: "Summarize this thread."}},
//	}, &out)
//	stream, err := ai.ChatStream(ctx, inference.Background, req)
//
// lodge knows the app by its socket peer's uid. Under LODGE_ROOT, where
// every app runs as the developer, the client also names its app in
// Lodge-App. It sends no key: lodge adds the provider's.
package inference

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/frostyard/lodge/kit/lodgeapp"
)

// Priority is a request's class in lodge's queue (Lodge-Priority).
type Priority string

const (
	// Interactive is work a person waits for: it goes first, and one slot
	// is always left for it.
	Interactive Priority = "interactive"
	// Background is everything else.
	Background Priority = "background"
)

// The headers the client sends.
const (
	HeaderPriority = "Lodge-Priority"
	HeaderApp      = "Lodge-App"
)

// Client calls lodge's inference socket. It is safe for concurrent use.
type Client struct {
	app  string // Lodge-App, or "" outside LODGE_ROOT
	http *http.Client
}

// New returns app id's client of the inference socket under env. Under
// env.Root it names the app in Lodge-App.
func New(env lodgeapp.Env, id string) *Client {
	if env.Root == "" {
		id = ""
	}
	return Dial(env.Path(lodgeapp.InferenceSocket), id)
}

// Dial returns a client of the inference socket at path, naming app in
// Lodge-App when app is not empty.
func Dial(path, app string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		DisableCompression: true,
		Protocols:          new(http.Protocols),
	}
	tr.Protocols.SetHTTP1(true)
	return &Client{app: app, http: &http.Client{Transport: tr}}
}

// Close releases the client's idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Model is an alias the app may use.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	// Available is false while the alias's provider cannot be reached; a
	// request to it then fails at once.
	Available bool `json:"available"`
}

// Models lists the aliases the app may use (GET /v1/models).
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/models", "", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var list struct {
		Data []Model `json:"data"`
	}
	if err := json.UnmarshalRead(resp.Body, &list); err != nil {
		return nil, fmt.Errorf("inference: models: %w", err)
	}
	return list.Data, nil
}

// Chat posts a chat completion (POST /v1/chat/completions) and decodes the
// answer into out, a *ChatCompletion or the app's own type. body is a
// ChatRequest or any JSON object; its stream member is set false.
func (c *Client) Chat(ctx context.Context, p Priority, body, out any) error {
	return c.call(ctx, "/v1/chat/completions", p, body, out)
}

// ChatStream posts a chat completion with stream set true, and returns its
// events: each a ChatChunk in Data, until [DONE].
func (c *Client) ChatStream(ctx context.Context, p Priority, body any) (*Stream, error) {
	return c.stream(ctx, "/v1/chat/completions", p, body)
}

// Respond posts to the responses API (POST /v1/responses) and decodes the
// answer into out. body is any JSON object; its stream member is set
// false.
func (c *Client) Respond(ctx context.Context, p Priority, body, out any) error {
	return c.call(ctx, "/v1/responses", p, body, out)
}

// RespondStream posts to the responses API with stream set true, and
// returns its events, such as response.output_text.delta and
// response.completed.
func (c *Client) RespondStream(ctx context.Context, p Priority, body any) (*Stream, error) {
	return c.stream(ctx, "/v1/responses", p, body)
}

func (c *Client) call(ctx context.Context, path string, p Priority, body, out any) error {
	raw, err := withStream(body, false)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, path, p, raw)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	if err := json.UnmarshalRead(resp.Body, out); err != nil {
		return fmt.Errorf("inference: %s: %w", path, err)
	}
	return nil
}

func (c *Client) stream(ctx context.Context, path string, p Priority, body any) (*Stream, error) {
	raw, err := withStream(body, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPost, path, p, raw)
	if err != nil {
		return nil, err
	}
	return newStream(resp.Body), nil
}

// withStream encodes body, a JSON object, with its stream member set.
func withStream(body any, stream bool) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("inference: encode the request: %w", err)
	}
	var obj map[string]jsontext.Value
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("inference: the request is not a JSON object")
	}
	obj["stream"] = jsontext.Value(fmt.Sprint(stream))
	return json.Marshal(obj, json.Deterministic(true))
}

// do sends a request with the client's headers, and returns a 2xx
// response; any other status is an *Error.
func (c *Client) do(ctx context.Context, method, path string, p Priority, body []byte) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://lodge"+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p != "" {
		req.Header.Set(HeaderPriority, string(p))
	}
	if c.app != "" {
		req.Header.Set(HeaderApp, c.app)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("inference: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		defer func() { _ = resp.Body.Close() }()
		return nil, readError(resp)
	}
	return resp, nil
}

// Error is lodge's or the provider's error answer, in OpenAI's shape.
type Error struct {
	// Status is the HTTP status: 404 for an alias the app may not use, 503
	// for one whose provider is unavailable, 429 when the queue is full.
	Status  int
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("inference: %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("inference: %d: %s", e.Status, e.Message)
}

func readError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var body struct {
		Error *Error `json:"error"`
	}
	if err := json.Unmarshal(data, &body); err == nil && body.Error != nil {
		body.Error.Status = resp.StatusCode
		return body.Error
	}
	msg := strings.TrimSpace(string(data))
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &Error{Status: resp.StatusCode, Message: msg}
}
