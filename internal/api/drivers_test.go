package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/rpc"
)

func TestDriversListReportsEveryRegisteredDriver(t *testing.T) {
	srv := rpc.NewServer()
	RegisterDrivers(srv)
	handler, ok := srv.Handler("drivers.list")
	if !ok {
		t.Fatal("drivers.list is not registered")
	}
	res, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("drivers.list: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got []struct {
		ID             string   `json:"id"`
		RequiredFields []string `json:"required_fields"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no drivers reported; the test would prove nothing")
	}
	var sqlite *struct {
		ID             string   `json:"id"`
		RequiredFields []string `json:"required_fields"`
	}
	for i := range got {
		if got[i].ID == "sqlite" {
			sqlite = &got[i]
		}
	}
	if sqlite == nil {
		t.Fatal("sqlite is registered but was not reported")
	}
	// The whole point: the UI learns the requirement from the engine rather
	// than keeping its own copy that can disagree.
	if !slices.Contains(sqlite.RequiredFields, "file") {
		t.Errorf("required_fields = %v, want it to include file", sqlite.RequiredFields)
	}
}

// Adversarial: required_fields must never marshal as null. The shell iterates
// it, and a driver with no requirements is a real case, not a hypothetical.
func TestDriversListMarshalsEmptyRequiredFieldsAsAnArray(t *testing.T) {
	// Unique per registration: driver.Register panics on a repeat id and
	// `go test -count=2` runs this twice in one process.
	id := fmt.Sprintf("needs-nothing#%d", noRequirementsSeq.Add(1))
	driver.Register(noRequirementsDriver{id: id})

	srv := rpc.NewServer()
	RegisterDrivers(srv)
	handler, _ := srv.Handler("drivers.list")
	res, err := handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("drivers.list: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Assert on the JSON bytes, not the Go slice: nil and an empty slice are
	// the same length in Go and different documents on the wire, and it is the
	// wire the shell reads.
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, e := range entries {
		if string(e["id"]) == strconv.Quote(id) {
			found = true
			if got := string(e["required_fields"]); got != "[]" {
				t.Errorf("required_fields = %s, want []", got)
			}
		}
	}
	if !found {
		t.Fatalf("the driver registered as %s was not reported", id)
	}
}

var noRequirementsSeq atomic.Int64

type noRequirementsDriver struct{ id string }

func (d noRequirementsDriver) ID() string                                { return d.id }
func (d noRequirementsDriver) Capabilities() driver.Capabilities         { return driver.Capabilities{} }
func (d noRequirementsDriver) RequiredFields(driver.ConnConfig) []string { return nil }
func (d noRequirementsDriver) Open(context.Context, driver.ConnConfig) (driver.Conn, error) {
	return nil, errors.New("noRequirementsDriver does not open")
}
