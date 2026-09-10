package dberr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestNewCarriesKindAndMessage(t *testing.T) {
	e := New(KindAuth, "access denied")
	if e.Kind != KindAuth {
		t.Errorf("kind = %q, want %q", e.Kind, KindAuth)
	}
	if e.Error() != "access denied" {
		t.Errorf("Error() = %q, want %q", e.Error(), "access denied")
	}
	if e.Native != "" {
		t.Errorf("Native = %q, want empty", e.Native)
	}
}

func TestWrapKeepsTheDriverText(t *testing.T) {
	e := Wrap(KindConstraint, "unique constraint violated", errors.New("UNIQUE constraint failed: users.email"))
	if e.Native != "UNIQUE constraint failed: users.email" {
		t.Errorf("Native = %q", e.Native)
	}
	if e.Message != "unique constraint violated" {
		t.Errorf("Message = %q", e.Message)
	}
}

func TestWrapToleratesANilCause(t *testing.T) {
	e := Wrap(KindUnknown, "something", nil)
	if e.Native != "" {
		t.Errorf("Native = %q, want empty", e.Native)
	}
}

func TestWithQueryDoesNotMutateTheOriginal(t *testing.T) {
	base := New(KindSyntax, "near SELECT")
	withQ := base.WithQuery("SELEC 1")

	if base.Query != "" {
		t.Errorf("original mutated: Query = %q", base.Query)
	}
	if withQ.Query != "SELEC 1" {
		t.Errorf("copy Query = %q", withQ.Query)
	}
	if withQ.Kind != KindSyntax || withQ.Message != "near SELECT" {
		t.Errorf("copy lost fields: %+v", withQ)
	}
}

// Cancellation must be distinguishable from failure — the UI must not paint
// the screen red when the user pressed Stop.
func TestFromMapsCancellationAndDeadline(t *testing.T) {
	if got := From(context.Canceled); got.Kind != KindCanceled {
		t.Errorf("context.Canceled -> %q, want %q", got.Kind, KindCanceled)
	}
	if got := From(context.DeadlineExceeded); got.Kind != KindTimeout {
		t.Errorf("context.DeadlineExceeded -> %q, want %q", got.Kind, KindTimeout)
	}
}

func TestFromPassesAnExistingErrorThrough(t *testing.T) {
	original := New(KindAuth, "access denied")
	if got := From(original); got != original {
		t.Errorf("From returned a different pointer for an *Error")
	}
}

// Drivers will wrap; errors.As must still find the Kind.
func TestFromUnwrapsAWrappedError(t *testing.T) {
	original := New(KindNetwork, "connection refused")
	got := From(fmt.Errorf("dialing: %w", original))
	if got.Kind != KindNetwork {
		t.Errorf("kind = %q, want %q", got.Kind, KindNetwork)
	}
}

func TestFromWrapsAnUnknownError(t *testing.T) {
	got := From(errors.New("boom"))
	if got.Kind != KindUnknown {
		t.Errorf("kind = %q, want %q", got.Kind, KindUnknown)
	}
	if got.Native != "boom" {
		t.Errorf("Native = %q, want boom", got.Native)
	}
}

func TestFromHandlesNil(t *testing.T) {
	if got := From(nil); got != nil {
		t.Errorf("From(nil) = %v, want nil", got)
	}
}

func TestJSONFieldNames(t *testing.T) {
	b, err := json.Marshal(New(KindTimeout, "took too long"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["kind"] != "timeout" {
		t.Errorf("kind = %v, want timeout", raw["kind"])
	}
	if _, ok := raw["message"]; !ok {
		t.Errorf("missing message in %s", b)
	}
	// native and query are omitempty — absent when unset.
	if _, ok := raw["native"]; ok {
		t.Errorf("native should be omitted when empty: %s", b)
	}
}
