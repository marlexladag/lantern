// Package dberr is the engine's normalized error type. Every driver maps its
// native failures onto a Kind so the UI can react without knowing which
// database produced the error.
//
// Spec section 11 is the authority. The rule that matters most: Canceled is
// not a failure. A user pressing Stop must not see an error state.
package dberr

import (
	"context"
	"errors"
)

// Kind classifies a failure. The UI branches on this.
type Kind string

const (
	KindAuth        Kind = "auth"
	KindNetwork     Kind = "network"
	KindSyntax      Kind = "syntax"
	KindConstraint  Kind = "constraint"
	KindTimeout     Kind = "timeout"
	KindCanceled    Kind = "canceled"
	KindNotFound    Kind = "not_found"
	KindUnsupported Kind = "unsupported"
	// KindInvalid is user-input validation, distinct from KindUnsupported:
	// unsupported means "this engine can't do that"; invalid means "what you
	// typed doesn't qualify", which the UI should render differently (spec
	// section 11 — the UI branches on Kind).
	KindInvalid Kind = "invalid"
	KindUnknown Kind = "unknown"
)

// Error is a driver failure in engine-neutral terms.
type Error struct {
	Kind    Kind   `json:"kind"`
	Message string `json:"message"`
	// Native is the driver's own text, shown only on request.
	Native string `json:"native,omitempty"`
	// Query is the statement that failed, when there was one.
	Query string `json:"query,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// New builds an error with no underlying cause.
func New(kind Kind, message string) *Error {
	return &Error{Kind: kind, Message: message}
}

// Wrap builds an error that keeps the driver's own text. A nil cause leaves
// Native empty rather than producing the string "<nil>".
func Wrap(kind Kind, message string, native error) *Error {
	e := &Error{Kind: kind, Message: message}
	if native != nil {
		e.Native = native.Error()
	}
	return e
}

// WithQuery returns a copy carrying the statement that failed. It copies so a
// shared sentinel cannot be mutated by whoever happens to report it.
func (e *Error) WithQuery(q string) *Error {
	c := *e
	c.Query = q
	return &c
}

// From normalizes any error. An *Error passes through unchanged, including one
// wrapped with %w. Context errors map to their own kinds so cancellation and
// timeout never read as unknown failures.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	switch {
	case errors.Is(err, context.Canceled):
		return Wrap(KindCanceled, "canceled", err)
	case errors.Is(err, context.DeadlineExceeded):
		return Wrap(KindTimeout, "timed out", err)
	}
	return Wrap(KindUnknown, err.Error(), err)
}
