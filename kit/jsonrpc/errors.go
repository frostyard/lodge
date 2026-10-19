package jsonrpc

import (
	"encoding/json/jsontext"
	"fmt"
)

// Code is an app's error-code type, such as its generated api.ErrorCode:
// an int whose String names the code.
type Code interface{ ~int }

// The JSON-RPC 2.0 error codes, and the one code of the apps' own that the
// server answers itself.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
	// CodeNotReady is the apps' notReady: a call before the connection's
	// hello (ServerOptions.Hello).
	CodeNotReady = 1004
)

// Error is a JSON-RPC error object, with the app's code type. Handlers
// return one to choose the code; the server answers any other error as
// internal. An app's api package names it: type Error = jsonrpc.Error[ErrorCode].
type Error[C Code] struct {
	Code    C              `json:"code"`
	Message string         `json:"message"`
	Data    jsontext.Value `json:"data,omitzero"`
}

// Errorf returns an *Error with a formatted message.
func Errorf[C Code](code C, format string, args ...any) *Error[C] {
	return &Error[C]{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Error is the code, by its String when it has one, and the message.
func (e *Error[C]) Error() string { return fmt.Sprintf("%v: %s", e.Code, e.Message) }

// Is matches a sentinel (an app's Err*) with the same code.
func (e *Error[C]) Is(target error) bool {
	t, ok := target.(*Error[C])
	return ok && t.Code == e.Code
}
