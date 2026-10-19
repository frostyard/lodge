package jsonrpc

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
)

// MaxMessageSize bounds one newline-delimited JSON message in either
// direction: 16 MiB, as a lodge /api frame.
const MaxMessageSize = 16 << 20

// Frame is one JSON-RPC 2.0 message in either direction: a request (ID and
// Method), a notification (Method, no ID) or a response (ID with Result or
// Error).
type Frame[C Code] struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id,omitzero"`
	Method  string         `json:"method,omitzero"`
	Params  jsontext.Value `json:"params,omitzero"`
	Result  jsontext.Value `json:"result,omitzero"`
	Error   *Error[C]      `json:"error,omitzero"`
}

// EventMethod is the method of the server's event notifications.
const EventMethod = "event"

// Event is an app's generated event type.
type Event interface {
	EventName() string
	Durable() bool
}

// EventEnvelope is the params of an "event" notification. Seq is set for
// durable events only.
type EventEnvelope struct {
	Seq   int64          `json:"seq,omitzero"`
	Event string         `json:"event"`
	Data  jsontext.Value `json:"data"`
}

// NewEnvelope encodes ev for delivery.
func NewEnvelope(seq int64, ev Event) (EventEnvelope, error) {
	data, err := json.Marshal(ev)
	if err != nil {
		return EventEnvelope{}, fmt.Errorf("encode event %s: %w", ev.EventName(), err)
	}
	return EventEnvelope{Seq: seq, Event: ev.EventName(), Data: data}, nil
}

// DecodeParams strictly decodes params into p: unknown members, and
// missing or null required members, are invalidParams. Absent or null
// params decode as {}.
func DecodeParams[C Code](raw jsontext.Value, p any, required []string) error {
	invalid := func(format string, args ...any) error { return Errorf(C(CodeInvalidParams), format, args...) }
	if len(raw) == 0 || string(raw) == "null" {
		raw = jsontext.Value("{}")
	}
	if raw.Kind() != '{' {
		return invalid("params must be an object")
	}
	if len(required) > 0 {
		var present map[string]jsontext.Value
		if err := json.Unmarshal(raw, &present); err != nil {
			return invalid("%v", err)
		}
		for _, name := range required {
			if v, ok := present[name]; !ok || string(v) == "null" {
				return invalid("missing required param %q", name)
			}
		}
	}
	if err := json.Unmarshal(raw, p, json.RejectUnknownMembers(true)); err != nil {
		return invalid("%v", err)
	}
	return nil
}
