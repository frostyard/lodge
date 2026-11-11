package inference

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

// These tests pin the stream's translation and the usage tap: what lodge
// sends an app, and what it records, for the same provider stream.

// textPart is an output item's content part, as outputItem holds it.
type textPart = struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// tokenCounts is a response object's usage, as responseObject holds it.
type tokenCounts = struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// incompleteDetails is a response object's incomplete details.
type incompleteDetails = struct {
	Reason string `json:"reason"`
}

// created is the time every chunk in these tests is stamped with.
const created = 1700000000

func messageItem(texts ...string) outputItem {
	it := outputItem{Type: "message"}
	for _, s := range texts {
		it.Content = append(it.Content, textPart{Type: "output_text", Text: s})
	}
	return it
}

func callItem(id, name, args string) outputItem {
	return outputItem{Type: "function_call", CallID: id, Name: name, Arguments: args}
}

// frame is one line of a streamed answer: its JSON by value, or [DONE].
type frame struct {
	done  bool
	value any
}

// parseFrames splits a streamed answer into its data lines and reads each
// by value: encoding/json/v2 does not sort a map's keys, so the raw text is
// no measure of a chunk.
func parseFrames(t *testing.T, out []byte) []frame {
	t.Helper()
	var fr []frame
	for _, part := range strings.Split(string(out), "\n\n") {
		if part == "" {
			continue
		}
		data, ok := strings.CutPrefix(part, "data: ")
		if !ok {
			t.Fatalf("a line without the %q prefix: %q", "data: ", part)
		}
		if data == "[DONE]" {
			fr = append(fr, frame{done: true})
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(data), &v); err != nil {
			t.Fatalf("a data line that is not JSON: %q: %v", data, err)
		}
		fr = append(fr, frame{value: v})
	}
	return fr
}

// sameFrames checks a streamed answer line by line: each JSON line by
// value, and the count and [DONE] exactly.
func sameFrames(t *testing.T, out []byte, want ...string) {
	t.Helper()
	got := parseFrames(t, out)
	if len(got) != len(want) {
		t.Fatalf("%d data lines, want %d: %q", len(got), len(want), out)
	}
	for i, w := range want {
		if w == "[DONE]" {
			if !got[i].done {
				t.Errorf("line %d is not [DONE]: %v", i, got[i].value)
			}
			continue
		}
		if got[i].done {
			t.Fatalf("line %d is [DONE], want %s", i, w)
		}
		var v any
		if err := json.Unmarshal([]byte(w), &v); err != nil {
			t.Fatalf("the expected line %d: %v", i, err)
		}
		if !reflect.DeepEqual(got[i].value, v) {
			t.Errorf("line %d:\n got %v\nwant %v", i, got[i].value, v)
		}
	}
}

func TestEventsReadsDataLines(t *testing.T) {
	answer := "event: response.output_text.delta\n" +
		`data: {"type":"a","delta":"hi"}` + "\n" +
		`data:{"type":"b"}` + "\r\n" +
		"data: [DONE]\n" +
		"\n" +
		"data:\n" +
		"data:    \n" +
		"data: not an object\n" +
		"data: [1,2]\n" +
		": a comment\n" +
		`data: {"type":"c"}`
	var types, raws []string
	err := events(strings.NewReader(answer), func(ev event, raw []byte) bool {
		types = append(types, ev.Type+" "+ev.Delta)
		raws = append(raws, string(raw))
		return true
	})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	wantTypes := []string{"a hi", "b ", "c "}
	wantRaws := []string{`{"type":"a","delta":"hi"}`, `{"type":"b"}`, `{"type":"c"}`}
	if !reflect.DeepEqual(types, wantTypes) || !reflect.DeepEqual(raws, wantRaws) {
		t.Errorf("events: %q with raw %q, want %q with %q", types, raws, wantTypes, wantRaws)
	}
}

func TestEventsStopsWhenFnReturnsFalse(t *testing.T) {
	answer := `data: {"type":"a"}` + "\n" + `data: {"type":"b"}` + "\n"
	var seen []string
	err := events(strings.NewReader(answer), func(ev event, _ []byte) bool {
		seen = append(seen, ev.Type)
		return false
	})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if want := []string{"a"}; !reflect.DeepEqual(seen, want) {
		t.Errorf("events read %q, want %q", seen, want)
	}
}

func TestEventsReturnsAReadError(t *testing.T) {
	boom := errors.New("boom")
	var seen int
	err := events(iotest.ErrReader(boom), func(event, []byte) bool {
		seen++
		return true
	})
	if err == nil {
		t.Fatal("events with a failing reader returned no error")
	}
	if seen != 0 {
		t.Errorf("events read %d events from a failing reader", seen)
	}
}

func TestUsageTapReadsStreamLines(t *testing.T) {
	tap := newTap(true)
	// One line in pieces, both forms of a stream's usage, and lines that
	// carry none.
	for _, p := range []string{
		"data: {\"usage\":{\"input_tokens\":7,\"outp",
		"ut_tokens\":3}}\r\n",
		"data: {\"type\":\"ping\"}\r\n",
		"data: {\"response\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":5}}}\r\n",
		"data: [DONE]\r\n",
		": a comment\r\n",
	} {
		tap.Write([]byte(p))
	}
	if tap.in != 11 || tap.out != 5 {
		t.Errorf("the tap read input %d output %d, want 11 and 5", tap.in, tap.out)
	}
}

func TestUsageTapWithoutUsage(t *testing.T) {
	tap := newTap(true)
	tap.Write([]byte("data: {\"choices\":[]}\r\n"))
	if tap.in != -1 || tap.out != -1 {
		t.Errorf("the tap read input %d output %d, want -1 and -1", tap.in, tap.out)
	}
}

func TestUsageTapReadsAWholeAnswer(t *testing.T) {
	tap := newTap(false)
	for _, p := range []string{
		`{"id":"chatcmpl-1","usa`,
		`ge":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13}}`,
	} {
		tap.Write([]byte(p))
	}
	tap.Finish()
	if tap.in != 9 || tap.out != 4 {
		t.Errorf("the tap read input %d output %d, want 9 and 4", tap.in, tap.out)
	}
}

func TestChatCompletionTextOnly(t *testing.T) {
	r := &responseObject{ID: "resp_1", Model: "gpt-5", Status: "completed"}
	got, err := json.Marshal(chatCompletion(r, []outputItem{messageItem("Hel", "lo")}, created))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"chatcmpl-resp_1","object":"chat.completion","created":1700000000,"model":"gpt-5",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]}`
	sameFrames(t, []byte("data: "+string(got)), want)
}

func TestChatCompletionToolCallsOnly(t *testing.T) {
	r := &responseObject{ID: "resp_1", Model: "gpt-5", Status: "completed"}
	items := []outputItem{callItem("call_a", "notes_search", ""), callItem("call_b", "notes_read", `{"id":"n1"}`)}
	got, err := json.Marshal(chatCompletion(r, items, created))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"chatcmpl-resp_1","object":"chat.completion","created":1700000000,"model":"gpt-5",
		"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[
			{"id":"call_a","type":"function","function":{"name":"notes_search","arguments":"{}"}},
			{"id":"call_b","type":"function","function":{"name":"notes_read","arguments":"{\"id\":\"n1\"}"}}]},
		"finish_reason":"tool_calls"}]}`
	sameFrames(t, []byte("data: "+string(got)), want)
}

func TestChatCompletionTextAndCalls(t *testing.T) {
	r := &responseObject{ID: "resp_1", Model: "gpt-5", Status: "completed",
		Usage: &tokenCounts{InputTokens: 10, OutputTokens: 4, TotalTokens: 14}}
	items := []outputItem{messageItem("Looking"), callItem("call_a", "notes_search", `{"q":"x"}`)}
	got, err := json.Marshal(chatCompletion(r, items, created))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"id":"chatcmpl-resp_1","object":"chat.completion","created":1700000000,"model":"gpt-5",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Looking","tool_calls":[
			{"id":"call_a","type":"function","function":{"name":"notes_search","arguments":"{\"q\":\"x\"}"}}]},
		"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`
	sameFrames(t, []byte("data: "+string(got)), want)
}

func TestChatCompletionUsesTheOutputWhenNoItemsCame(t *testing.T) {
	r := &responseObject{ID: "resp_1", Model: "gpt-5", Status: "completed",
		Output: []outputItem{messageItem("whole"), callItem("call_a", "notes_search", `{"q":"x"}`)}}
	got, err := json.Marshal(chatCompletion(r, nil, created))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// An empty item list falls back to the output too.
	empty := chatCompletion(r, []outputItem{}, created)
	if !reflect.DeepEqual(empty, chatCompletion(r, r.Output, created)) {
		t.Errorf("an empty item list did not fall back to the output")
	}
	want := `{"id":"chatcmpl-resp_1","object":"chat.completion","created":1700000000,"model":"gpt-5",
		"choices":[{"index":0,"message":{"role":"assistant","content":"whole","tool_calls":[
			{"id":"call_a","type":"function","function":{"name":"notes_search","arguments":"{\"q\":\"x\"}"}}]},
		"finish_reason":"tool_calls"}]}`
	sameFrames(t, []byte("data: "+string(got)), want)
}

func TestChatCompletionFinishReasons(t *testing.T) {
	for _, tc := range []struct {
		name  string
		r     *responseObject
		items []outputItem
		want  string
	}{
		{"a completed response", &responseObject{Status: "completed"}, nil, "stop"},
		{"a called tool", &responseObject{Status: "completed"}, []outputItem{callItem("c", "f", "{}")}, "tool_calls"},
		{"a filtered response", &responseObject{Status: "incomplete",
			IncompleteDetails: &incompleteDetails{Reason: "content_filter"}}, nil, "content_filter"},
		{"a cut response", &responseObject{Status: "incomplete",
			IncompleteDetails: &incompleteDetails{Reason: "max_output_tokens"}}, nil, "length"},
		{"a cut response without details", &responseObject{Status: "incomplete"}, nil, "length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := chatCompletion(tc.r, tc.items, created)
			choices := out["choices"].([]map[string]any)
			if got := choices[0]["finish_reason"]; got != tc.want {
				t.Errorf("finish_reason %v, want %s", got, tc.want)
			}
		})
	}
}

// runChunker feeds events to a chunker and returns what it wrote.
func runChunker(t *testing.T, usage bool, evs ...event) []byte {
	t.Helper()
	var buf bytes.Buffer
	c := &chunker{w: &buf, flush: func() {}, created: created, usage: usage}
	for _, ev := range evs {
		done, err := c.event(ev)
		if err != nil {
			t.Fatalf("%s: %v", ev.Type, err)
		}
		if done {
			break
		}
	}
	return buf.Bytes()
}

// createdEvent opens a stream with the response's id and model.
func createdEvent() event {
	return event{Type: "response.created", Response: &responseObject{ID: "resp_1", Model: "gpt-5"}}
}

const chunkHead = `"id":"chatcmpl-resp_1","object":"chat.completion.chunk","created":1700000000,"model":"gpt-5"`

func TestChunkerTextDeltas(t *testing.T) {
	out := runChunker(t, false,
		createdEvent(),
		event{Type: "response.output_text.delta", Delta: "Hel"},
		event{Type: "response.output_text.delta", Delta: "lo"},
		event{Type: "response.completed", Response: &responseObject{ID: "resp_1", Model: "gpt-5", Status: "completed"}},
	)
	sameFrames(t, out,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{"content":"Hel","role":"assistant"},"finish_reason":null}]}`,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"[DONE]")
}

func TestChunkerFunctionCallDone(t *testing.T) {
	item := callItem("call_a", "notes_search", `{"q":"x"}`)
	out := runChunker(t, false,
		createdEvent(),
		event{Type: "response.output_item.done", Item: &item},
		event{Type: "response.completed", Response: &responseObject{Status: "completed"}},
	)
	sameFrames(t, out,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_a","type":"function","function":{"name":"notes_search","arguments":"{\"q\":\"x\"}"}}]},
			"finish_reason":null}]}`,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"[DONE]")
}

func TestChunkerWholeAnswerFromTextItems(t *testing.T) {
	first, second := messageItem("Hel"), messageItem("lo")
	out := runChunker(t, false,
		createdEvent(),
		event{Type: "response.output_item.done", Item: &first},
		event{Type: "response.output_item.done", Item: &second},
		event{Type: "response.completed", Response: &responseObject{Status: "completed"}},
	)
	sameFrames(t, out,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{"content":"Hello","role":"assistant"},"finish_reason":null}]}`,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"[DONE]")
}

func TestChunkerFunctionCallItemsWithoutDeltas(t *testing.T) {
	a, b := callItem("call_a", "notes_search", ""), callItem("call_b", "notes_read", `{"id":"n1"}`)
	out := runChunker(t, false,
		createdEvent(),
		event{Type: "response.output_item.done", Item: &a},
		event{Type: "response.output_item.done", Item: &b},
		event{Type: "response.completed", Response: &responseObject{Status: "completed"}},
	)
	sameFrames(t, out,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[
			{"index":0,"id":"call_a","type":"function","function":{"name":"notes_search","arguments":"{}"}}]},
			"finish_reason":null}]}`,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{"tool_calls":[
			{"index":1,"id":"call_b","type":"function","function":{"name":"notes_read","arguments":"{\"id\":\"n1\"}"}}]},
			"finish_reason":null}]}`,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"[DONE]")
}

func TestChunkerWholeAnswerFromTheResponseOutput(t *testing.T) {
	// An unstored response's completed event carries no items of its own;
	// these come from its output.
	r := &responseObject{ID: "resp_1", Model: "gpt-5", Status: "completed",
		Output: []outputItem{messageItem("whole"), callItem("call_a", "notes_search", `{"q":"x"}`)}}
	out := runChunker(t, false, event{Type: "response.completed", Response: r})
	sameFrames(t, out,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{"content":"whole","role":"assistant","tool_calls":[
			{"index":0,"id":"call_a","type":"function","function":{"name":"notes_search","arguments":"{\"q\":\"x\"}"}}]},
			"finish_reason":null}]}`,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"[DONE]")
}

func TestChunkerUsageChunk(t *testing.T) {
	r := &responseObject{ID: "resp_1", Model: "gpt-5", Status: "completed",
		Usage: &tokenCounts{InputTokens: 10, OutputTokens: 4, TotalTokens: 14}}
	out := runChunker(t, true,
		createdEvent(),
		event{Type: "response.output_text.delta", Delta: "hi"},
		event{Type: "response.completed", Response: r},
	)
	sameFrames(t, out,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{"content":"hi","role":"assistant"},"finish_reason":null}]}`,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{`+chunkHead+`,"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`,
		"[DONE]")
}

func TestChunkerUsageAskedAndNoneCame(t *testing.T) {
	out := runChunker(t, true,
		createdEvent(),
		event{Type: "response.output_text.delta", Delta: "hi"},
		event{Type: "response.completed", Response: &responseObject{Status: "completed"}},
	)
	sameFrames(t, out,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{"content":"hi","role":"assistant"},"finish_reason":null}]}`,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"[DONE]")
}

func TestChunkerCompletedWithoutAResponse(t *testing.T) {
	out := runChunker(t, false, createdEvent(), event{Type: "response.completed"})
	sameFrames(t, out,
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":"stop"}]}`,
		"[DONE]")
}

func TestChunkerFailedResponse(t *testing.T) {
	failed := event{Type: "response.failed", Response: &responseObject{
		Error: &apiErr{Code: "subscription_sharing_usage_limit_exceeded", Message: "the limit"}}}
	out := runChunker(t, false, createdEvent(), failed)
	sameFrames(t, out, `{"error":{"message":"the ChatGPT plan's usage limit for lodge is reached `+
		`(ChatGPT settings, Usage); no other provider is tried","type":"server_error",`+
		`"code":"subscription_sharing_usage_limit_exceeded"}}`)
}

func TestChunkerFailureWithoutAResponse(t *testing.T) {
	out := runChunker(t, false, createdEvent(),
		event{Type: "error", Error: &apiErr{Code: "server_error", Message: "boom"}})
	sameFrames(t, out, `{"error":{"message":"boom","type":"server_error","code":"server_error"}}`)
}

func TestChunkerSkipsDoneLineOfItsInputStream(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5"}}` + "\n" +
		`data: {"type":"response.output_text.delta","delta":"hi"}` + "\n" +
		"data: [DONE]\n"
	var buf bytes.Buffer
	c := &chunker{w: &buf, flush: func() {}, created: created}
	var last bool
	if err := events(strings.NewReader(stream), func(ev event, _ []byte) bool {
		done, err := c.event(ev)
		if err != nil {
			t.Fatalf("%s: %v", ev.Type, err)
		}
		last = done
		return !done
	}); err != nil {
		t.Fatalf("events: %v", err)
	}
	if last {
		t.Error("the stream's [DONE] line ended the answer")
	}
	sameFrames(t, buf.Bytes(),
		`{`+chunkHead+`,"choices":[{"index":0,"delta":{"content":"hi","role":"assistant"},"finish_reason":null}]}`)
}
