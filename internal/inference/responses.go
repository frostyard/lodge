package inference

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"
)

// A codex provider speaks only the Responses API, streamed and unstored,
// with the plan's limits (docs/specs/inference.md, The codex provider):
// lodge turns an app's chat completion into a Responses request, and the
// stream of events back into a chat completion or its chunks.

// unsupported are the Responses fields plan usage refuses
// (https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations).
var unsupported = []string{"background", "conversation", "max_output_tokens", "max_tool_calls", "metadata", "moderation",
	"multi_agent", "prompt", "prompt_cache_retention", "safety_identifier", "temperature", "top_logprobs", "top_p",
	"truncation", "user", "previous_response_id"}

// defaultNamespace holds the functions whose names name no app.
const defaultNamespace = "functions"

// toolNames maps the functions a Responses answer may call back to the
// app's names: plan usage takes functions grouped in namespaces, so
// "notes__read_note" goes as read_note in namespace notes.
type toolNames struct {
	byPair map[[2]string]string // {namespace, name} → the app's name
	byName map[string][]string  // name → the app's names
}

// split is an app's function name as a namespace and a name.
func split(full string) (ns, name string) {
	if ns, name, ok := strings.Cut(full, "__"); ok && ns != "" && name != "" {
		return ns, name
	}
	return defaultNamespace, full
}

func (n *toolNames) add(full string) {
	ns, name := split(full)
	n.byPair[[2]string{ns, name}] = full
	n.byName[name] = append(n.byName[name], full)
}

// app names a called function as the app knows it.
func (n *toolNames) app(ns, name string) string {
	if ns == "" {
		if a, b, ok := strings.Cut(name, "."); ok {
			ns, name = a, b
		}
	}
	if full, ok := n.byPair[[2]string{ns, name}]; ok {
		return full
	}
	if list := n.byName[name]; len(list) == 1 {
		return list[0]
	}
	if ns != "" && ns != defaultNamespace {
		return ns + "__" + name
	}
	return name
}

// chatMessage is a chat completion's message, as lodge reads it.
type chatMessage struct {
	Role       string         `json:"role"`
	Content    jsontext.Value `json:"content"`
	ToolCallID string         `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// chatRequest is the part of a chat completion request lodge translates.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  jsontext.Value `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
	ToolChoice        jsontext.Value `json:"tool_choice"`
	ParallelToolCalls *bool          `json:"parallel_tool_calls"`
	ReasoningEffort   string         `json:"reasoning_effort"`
	StreamOptions     *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

// errUnsupported is a request plan usage cannot carry.
type errUnsupported struct{ what string }

func (e errUnsupported) Error() string { return "the codex provider does not take " + e.what }

// text is a message content's text: a string, or the text of its parts.
func text(v jsontext.Value) (string, error) {
	if len(v) == 0 || string(v) == "null" {
		return "", nil
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(v, &parts); err != nil {
		return "", errUnsupported{"this message content"}
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			return "", errUnsupported{p.Type + " content here"}
		}
		b.WriteString(p.Text)
	}
	return b.String(), nil
}

// userContent is a user message's content in Responses' terms.
func userContent(v jsontext.Value) (any, error) {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s, nil
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(v, &parts); err != nil {
		return nil, errUnsupported{"this user content"}
	}
	out := make([]map[string]string, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text":
			out = append(out, map[string]string{"type": "input_text", "text": p.Text})
		case "image_url":
			out = append(out, map[string]string{"type": "input_image", "image_url": p.ImageURL.URL})
		default:
			return nil, errUnsupported{p.Type + " content"}
		}
	}
	return out, nil
}

// chatToResponses translates a chat completion request, whose model is
// already the provider's, into a Responses request.
func chatToResponses(body []byte) ([]byte, *toolNames, *chatRequest, error) {
	var in chatRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, nil, nil, fmt.Errorf("the body: %w", err)
	}
	names := &toolNames{byPair: map[[2]string]string{}, byName: map[string][]string{}}
	var instructions []string
	input := []any{}
	for i, m := range in.Messages {
		switch m.Role {
		case "system", "developer":
			t, err := text(m.Content)
			if err != nil {
				return nil, nil, nil, err
			}
			if len(input) == 0 {
				instructions = append(instructions, t)
			} else {
				input = append(input, map[string]any{"role": "developer", "content": t})
			}
		case "user":
			c, err := userContent(m.Content)
			if err != nil {
				return nil, nil, nil, err
			}
			input = append(input, map[string]any{"role": "user", "content": c})
		case "assistant":
			t, err := text(m.Content)
			if err != nil {
				return nil, nil, nil, err
			}
			if t != "" {
				input = append(input, map[string]any{"role": "assistant", "content": t})
			}
			for _, tc := range m.ToolCalls {
				ns, name := split(tc.Function.Name)
				input = append(input, map[string]any{"type": "function_call", "call_id": tc.ID, "namespace": ns, "name": name,
					"arguments": cmpString(tc.Function.Arguments, "{}")})
			}
		case "tool":
			t, err := text(m.Content)
			if err != nil {
				return nil, nil, nil, err
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": t})
		default:
			return nil, nil, nil, errUnsupported{fmt.Sprintf("messages[%d]'s role %q", i, m.Role)}
		}
	}
	out := map[string]any{"model": in.Model, "input": input, "store": false, "stream": true}
	if len(instructions) > 0 {
		out["instructions"] = strings.Join(instructions, "\n\n")
	}
	if len(in.Tools) > 0 {
		groups := map[string][]map[string]any{}
		var order []string
		for _, t := range in.Tools {
			if t.Type != "function" {
				return nil, nil, nil, errUnsupported{"tools of type " + t.Type}
			}
			names.add(t.Function.Name)
			ns, name := split(t.Function.Name)
			if _, ok := groups[ns]; !ok {
				order = append(order, ns)
			}
			fn := map[string]any{"type": "function", "name": name, "description": t.Function.Description}
			if len(t.Function.Parameters) > 0 {
				fn["parameters"] = t.Function.Parameters
			} else {
				fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			groups[ns] = append(groups[ns], fn)
		}
		tools := make([]map[string]any, 0, len(order))
		for _, ns := range order {
			desc := "Tools of " + ns + "."
			if ns == defaultNamespace {
				desc = "Tools."
			}
			tools = append(tools, map[string]any{"type": "namespace", "name": ns, "description": desc, "tools": groups[ns]})
		}
		out["tools"] = tools
	}
	if len(in.ToolChoice) > 0 && string(in.ToolChoice) != "null" {
		var s string
		if json.Unmarshal(in.ToolChoice, &s) == nil {
			out["tool_choice"] = s // auto, none or required, as in Responses
		} else {
			out["tool_choice"] = "required" // one named function: at least make it call one
		}
	}
	if in.ParallelToolCalls != nil {
		out["parallel_tool_calls"] = *in.ParallelToolCalls
	}
	if in.ReasoningEffort != "" {
		out["reasoning"] = map[string]string{"effort": in.ReasoningEffort}
	}
	b, err := json.Marshal(out)
	return b, names, &in, err
}

func cmpString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// sanitizeResponses fits an app's own Responses request to plan usage:
// streamed, unstored, without the fields it refuses, and with system
// messages as developer messages.
func sanitizeResponses(members map[string]jsontext.Value) ([]byte, error) {
	for _, k := range unsupported {
		delete(members, k)
	}
	members["store"] = jsontext.Value("false")
	members["stream"] = jsontext.Value("true")
	if raw, ok := members["input"]; ok {
		var items []map[string]jsontext.Value
		if json.Unmarshal(raw, &items) == nil {
			for _, it := range items {
				var role string
				if json.Unmarshal(it["role"], &role) == nil && role == "system" {
					it["role"] = jsontext.Value(`"developer"`)
				}
			}
			b, err := json.Marshal(items)
			if err != nil {
				return nil, err
			}
			members["input"] = b
		}
	}
	return json.Marshal(members)
}

// --- The stream back ---

// event is one Responses stream event, as lodge reads it.
type event struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
	Item  *struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	Response *responseObject `json:"response"`
	// Error is an error event's, and Code and Message too for some.
	Error   *apiErr `json:"error"`
	Code    string  `json:"code"`
	Message string  `json:"message"`
}

type apiErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
}

type responseObject struct {
	ID                string `json:"id"`
	Model             string `json:"model"`
	CreatedAt         int64  `json:"created_at"`
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error  *apiErr `json:"error"`
	Output []struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage *struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	} `json:"usage"`
}

// events reads a Responses stream's events, each with its raw data, and
// calls fn until it returns false or the stream ends.
func events(r io.Reader, fn func(ev event, raw []byte) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		data, ok := bytes.CutPrefix(sc.Bytes(), []byte("data:"))
		if !ok {
			continue
		}
		data = bytes.TrimSpace(data)
		if len(data) == 0 || data[0] != '{' {
			continue
		}
		var ev event
		if json.Unmarshal(data, &ev) != nil {
			continue
		}
		if !fn(ev, data) {
			return nil
		}
	}
	return sc.Err()
}

// failure is a stream's failure: its code and message.
func (ev event) failure() (apiErr, bool) {
	switch {
	case ev.Type == "response.failed" && ev.Response != nil && ev.Response.Error != nil:
		return *ev.Response.Error, true
	case ev.Type == "response.failed":
		return apiErr{Code: "response_failed", Message: "the response failed"}, true
	case ev.Type == "error" && ev.Error != nil:
		return *ev.Error, true
	case ev.Type == "error":
		return apiErr{Code: ev.Code, Message: ev.Message}, true
	}
	return apiErr{}, false
}

// planStatus is the HTTP status for a plan usage error code
// (https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery).
func planStatus(code string) int {
	switch code {
	case "subscription_sharing_usage_limit_exceeded":
		return 429
	case "subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable":
		return 503
	case "subscription_sharing_user_not_eligible", "subscription_sharing_route_not_supported",
		"chatpass_v2_scope_not_authorized", "chatpass_v2_invalid_authorization_context":
		return 403
	case "subscription_sharing_unsupported_capability":
		return 400
	case "subscription_sharing_invalid_user":
		return 401
	}
	return 502
}

// planMessage is what an app reads for a plan usage error.
func planMessage(e apiErr) string {
	switch e.Code {
	case "subscription_sharing_usage_limit_exceeded":
		return "the ChatGPT plan's usage limit for lodge is reached (ChatGPT settings, Usage); no other provider is tried"
	case "subscription_sharing_unsupported_capability":
		return "the ChatGPT plan does not take this request's " + cmpString(e.Param, "content") + ": " + e.Message
	}
	return cmpString(e.Message, e.Code)
}

// chatCompletion assembles a finished response as a chat completion.
func chatCompletion(r *responseObject, names *toolNames, created int64) map[string]any {
	var b strings.Builder
	var calls []map[string]any
	for _, o := range r.Output {
		switch o.Type {
		case "message":
			for _, c := range o.Content {
				if c.Type == "output_text" {
					b.WriteString(c.Text)
				}
			}
		case "function_call":
			calls = append(calls, map[string]any{"id": o.CallID, "type": "function",
				"function": map[string]string{"name": names.app(o.Namespace, o.Name), "arguments": cmpString(o.Arguments, "{}")}})
		}
	}
	msg := map[string]any{"role": "assistant", "content": nil}
	if b.Len() > 0 {
		msg["content"] = b.String()
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	out := map[string]any{"id": "chatcmpl-" + r.ID, "object": "chat.completion", "created": created, "model": r.Model,
		"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": finishReason(r, len(calls) > 0)}}}
	if u := r.Usage; u != nil {
		out["usage"] = map[string]int64{"prompt_tokens": u.InputTokens, "completion_tokens": u.OutputTokens,
			"total_tokens": u.InputTokens + u.OutputTokens}
	}
	return out
}

func finishReason(r *responseObject, called bool) string {
	switch {
	case called:
		return "tool_calls"
	case r.Status == "incomplete" && r.IncompleteDetails != nil && r.IncompleteDetails.Reason == "content_filter":
		return "content_filter"
	case r.Status == "incomplete":
		return "length"
	}
	return "stop"
}

// chunker writes a Responses stream to an app as chat completion chunks.
type chunker struct {
	w        io.Writer
	flush    func()
	names    *toolNames
	created  int64
	usage    bool // the app asked for a last chunk with usage
	id       string
	model    string
	started  bool
	sentText bool
	calls    int
}

func (c *chunker) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.w, "data: %s\n\n", b); err != nil {
		return err
	}
	c.flush()
	return nil
}

func (c *chunker) chunk(delta map[string]any, finish any) error {
	if !c.started {
		c.started = true
		delta["role"] = "assistant"
	}
	return c.write(map[string]any{"id": "chatcmpl-" + c.id, "object": "chat.completion.chunk", "created": c.created,
		"model": c.model, "choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}}})
}

// event handles one event; done reports the stream's end.
func (c *chunker) event(ev event) (done bool, err error) {
	if r := ev.Response; r != nil {
		c.id, c.model = cmpString(c.id, r.ID), cmpString(c.model, r.Model)
	}
	switch ev.Type {
	case "response.output_text.delta":
		c.sentText = true
		return false, c.chunk(map[string]any{"content": ev.Delta}, nil)
	case "response.output_item.done":
		if ev.Item == nil || ev.Item.Type != "function_call" {
			return false, nil
		}
		call := map[string]any{"index": c.calls, "id": ev.Item.CallID, "type": "function",
			"function": map[string]string{"name": c.names.app(ev.Item.Namespace, ev.Item.Name), "arguments": cmpString(ev.Item.Arguments, "{}")}}
		c.calls++
		return false, c.chunk(map[string]any{"tool_calls": []any{call}}, nil)
	case "response.completed", "response.incomplete":
		r := ev.Response
		if r == nil {
			r = &responseObject{Status: "completed"}
		}
		if !c.sentText && c.calls == 0 {
			// No deltas came: send the whole answer as one chunk.
			whole := chatCompletion(r, c.names, c.created)
			msg := whole["choices"].([]map[string]any)[0]["message"].(map[string]any)
			delta := map[string]any{}
			if msg["content"] != nil {
				delta["content"] = msg["content"]
			}
			if tc, ok := msg["tool_calls"]; ok {
				calls := tc.([]map[string]any)
				for i := range calls {
					calls[i]["index"] = i
				}
				delta["tool_calls"] = calls
				c.calls = len(calls)
			}
			if len(delta) > 0 {
				if err := c.chunk(delta, nil); err != nil {
					return true, err
				}
			}
		}
		if err := c.chunk(map[string]any{}, finishReason(r, c.calls > 0)); err != nil {
			return true, err
		}
		if c.usage && r.Usage != nil {
			if err := c.write(map[string]any{"id": "chatcmpl-" + c.id, "object": "chat.completion.chunk", "created": c.created,
				"model": c.model, "choices": []any{}, "usage": map[string]int64{"prompt_tokens": r.Usage.InputTokens,
					"completion_tokens": r.Usage.OutputTokens, "total_tokens": r.Usage.InputTokens + r.Usage.OutputTokens}}); err != nil {
				return true, err
			}
		}
		_, err := io.WriteString(c.w, "data: [DONE]\n\n")
		c.flush()
		return true, err
	}
	if e, failed := ev.failure(); failed {
		return true, c.write(map[string]any{"error": map[string]any{"message": planMessage(e), "type": "server_error", "code": e.Code}})
	}
	return false, nil
}

// errEarlyEnd is a stream that ended before response.completed: plan
// usage counts only a completed response as done.
var errEarlyEnd = errors.New("the provider's stream ended before the response completed")
