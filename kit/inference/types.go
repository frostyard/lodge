package inference

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
)

// Extra carries the members a type does not name, such as a provider's
// own parameters (the local model's thinking control) or tool calls; they
// pass through unchanged.
type Extra = map[string]jsontext.Value

// ChatRequest is a chat completion's request body. lodge replaces Model,
// an alias, with the provider's model.
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Extra    Extra     `json:",embed"`
}

// Message is one chat message. Content is text; a message of another
// shape, such as content parts, goes in a request of the app's own type.
type Message struct {
	Role    string `json:"role,omitzero"`
	Content string `json:"content"`
	Extra   Extra  `json:",embed"`
}

// ChatCompletion is a chat completion's answer.
type ChatCompletion struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitzero"`
	Extra   Extra    `json:",embed"`
}

// Choice is one of a completion's answers.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
	Extra        Extra   `json:",embed"`
}

// ChatChunk is one event of a streamed chat completion.
type ChatChunk struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
	// Usage comes in the last chunk, when the request asked for it
	// (stream_options.include_usage).
	Usage *Usage `json:"usage,omitzero"`
	Extra Extra  `json:",embed"`
}

// ChunkChoice is one answer's part in a chunk.
type ChunkChoice struct {
	Index        int     `json:"index"`
	Delta        Message `json:"delta"`
	FinishReason *string `json:"finish_reason"`
	Extra        Extra   `json:",embed"`
}

// Usage is an answer's token counts: prompt and completion tokens for chat
// completions, input and output tokens for responses.
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens,omitzero"`
	CompletionTokens int64 `json:"completion_tokens,omitzero"`
	InputTokens      int64 `json:"input_tokens,omitzero"`
	OutputTokens     int64 `json:"output_tokens,omitzero"`
	TotalTokens      int64 `json:"total_tokens,omitzero"`
	Extra            Extra `json:",embed"`
}

// Event is one server-sent event of a stream: its type (event:, empty for
// chat chunks) and its data.
type Event struct {
	Type string
	Data jsontext.Value
}

// Decode decodes the event's data into v.
func (e Event) Decode(v any) error { return json.Unmarshal(e.Data, v) }

// MaxEvent bounds one event's data: 32 MiB.
const MaxEvent = 32 << 20

// Stream reads a streamed answer's events. It is not safe for concurrent
// use; Close it when done, or cancel the request's context.
type Stream struct {
	body io.ReadCloser
	sc   *bufio.Scanner
	done bool
}

func newStream(body io.ReadCloser) *Stream {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), MaxEvent)
	return &Stream{body: body, sc: sc}
}

// Next returns the next event, or io.EOF after the last: after a chat
// stream's [DONE], or when the answer ends.
func (s *Stream) Next() (Event, error) {
	if s.done {
		return Event{}, io.EOF
	}
	var ev Event
	var data [][]byte
	for s.sc.Scan() {
		line := s.sc.Bytes()
		switch {
		case len(line) == 0:
			if len(data) == 0 {
				ev.Type = ""
				continue // a blank line with nothing to dispatch
			}
			joined := bytes.Join(data, []byte("\n"))
			if string(joined) == "[DONE]" {
				s.done = true
				return Event{}, io.EOF
			}
			if !jsontext.Value(joined).IsValid() {
				return Event{}, fmt.Errorf("inference: an event that is not JSON: %.80q", joined)
			}
			ev.Data = jsontext.Value(bytes.Clone(joined))
			return ev, nil
		case line[0] == ':':
			// a comment, such as a keepalive
		default:
			field, value, _ := bytes.Cut(line, []byte(":"))
			value = bytes.TrimPrefix(value, []byte(" "))
			switch string(field) {
			case "event":
				ev.Type = string(value)
			case "data":
				data = append(data, bytes.Clone(value))
			}
		}
	}
	s.done = true
	if err := s.sc.Err(); err != nil {
		return Event{}, fmt.Errorf("inference: the stream: %w", err)
	}
	if len(data) > 0 {
		return Event{}, errors.New("inference: the stream ended inside an event")
	}
	return Event{}, io.EOF
}

// Close ends the stream.
func (s *Stream) Close() error {
	s.done = true
	return s.body.Close()
}
