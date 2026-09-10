// Package rpc implements newline-delimited JSON-RPC 2.0 over a byte stream.
//
// The engine speaks this protocol on stdin and stdout. Nothing else may be
// written to stdout: a single stray write corrupts the stream for the shell.
package rpc

import "encoding/json"

// Version is the only JSON-RPC version this implementation accepts.
const Version = "2.0"

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// Request is an incoming call. A nil ID marks a notification, which must not
// receive a response.
type Request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

// IsNotification reports whether the request expects no response.
func (r *Request) IsNotification() bool { return r.ID == nil }

// Response is an outgoing reply. Exactly one of Result or Error is set.
type Response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
}

// Error is a JSON-RPC error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Errorf builds an Error with no data payload.
func Errorf(code int, msg string) *Error {
	return &Error{Code: code, Message: msg}
}
