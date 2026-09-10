package driver

import (
	"context"
	"strings"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/schema"
)

type stubDriver struct{ id string }

func (s stubDriver) ID() string                 { return s.id }
func (s stubDriver) Capabilities() Capabilities { return Capabilities{} }
func (s stubDriver) Open(context.Context, ConnConfig) (Conn, error) {
	return nil, nil
}

func TestRegisterAndLookup(t *testing.T) {
	reset()
	Register(stubDriver{id: "stub"})

	got, ok := Lookup("stub")
	if !ok {
		t.Fatal("Lookup(stub) not found after Register")
	}
	if got.ID() != "stub" {
		t.Errorf("id = %q, want stub", got.ID())
	}
	if _, ok := Lookup("absent"); ok {
		t.Error("Lookup(absent) reported found")
	}
}

func TestIDsAreSorted(t *testing.T) {
	reset()
	Register(stubDriver{id: "sqlite"})
	Register(stubDriver{id: "mysql"})
	Register(stubDriver{id: "postgres"})

	got := IDs()
	want := []string{"mysql", "postgres", "sqlite"}
	if len(got) != len(want) {
		t.Fatalf("IDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("IDs() = %v, want %v", got, want)
		}
	}
}

// The interfaces must be satisfiable — this fails to compile if a signature
// drifts, which is the point.
func TestStubSatisfiesDriver(t *testing.T) {
	var _ Driver = stubDriver{}
	var _ schema.TableKind = schema.TableKindTable
}

// Registration happens from init() across packages, so a duplicate id is a
// programming error, not a runtime condition: Register must panic rather
// than let the last init silently win.
func TestRegisterPanicsOnDuplicateID(t *testing.T) {
	reset()
	Register(stubDriver{id: "dup"})

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Register did not panic on a duplicate id")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "dup") {
			t.Errorf("panic value = %v, want a message naming the duplicate id %q", r, "dup")
		}
	}()
	Register(stubDriver{id: "dup"})
}
