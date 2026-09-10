package api

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/store"
	"github.com/marlexladag/lantern/internal/rpc"
)

type saveParams struct {
	Connection store.Saved `json:"connection"`
	Password   string      `json:"password"`
}

type idParams struct {
	ID string `json:"id"`
}

// testResult is a normal result, not an error: a failed connection test is
// something the dialog renders inline, not a transport failure.
type testResult struct {
	OK    bool   `json:"ok"`
	Kind  string `json:"kind,omitempty"`
	Error string `json:"error,omitempty"`
}

// RegisterConnections wires the connection CRUD methods onto srv.
func RegisterConnections(srv *rpc.Server, st *store.Store) {
	srv.Register("connections.list", func(ctx context.Context, _ json.RawMessage) (any, error) {
		records, err := st.List()
		if err != nil {
			return nil, ToRPCError(err)
		}
		return records, nil
	})

	srv.Register("connections.save", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p saveParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "connections.save: "+err.Error())
		}
		if p.Connection.Name == "" {
			return nil, ToRPCError(dberr.New(dberr.KindInvalid, "a connection needs a name"))
		}
		if err := checkConnectionIsSavable(p.Connection); err != nil {
			return nil, ToRPCError(err)
		}
		saved, err := st.Save(p.Connection, p.Password)
		if err != nil {
			return nil, ToRPCError(err)
		}
		return saved, nil
	})

	srv.Register("connections.delete", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p idParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "connections.delete: "+err.Error())
		}
		if err := st.Delete(p.ID); err != nil {
			return nil, ToRPCError(err)
		}
		return map[string]bool{"deleted": true}, nil
	})

	srv.Register("connections.test", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p saveParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "connections.test: "+err.Error())
		}
		conn, _, err := dial(ctx, p.Connection, p.Password)
		if err != nil {
			e := dberr.From(err)
			return testResult{OK: false, Kind: string(e.Kind), Error: e.Message}, nil
		}
		defer conn.Close()
		if err := conn.Ping(ctx); err != nil {
			e := dberr.From(err)
			return testResult{OK: false, Kind: string(e.Kind), Error: e.Message}, nil
		}
		return testResult{OK: true}, nil
	})
}

// checkConnectionIsSavable rejects a connection at save time when it can
// never dial: either its driver id is not registered with this engine, or
// its own driver reports a required field is missing. Both used to surface
// much later instead, after the record was already durable — a missing
// field as a confusing "no database file given" the first time something
// tried to open it, and an unregistered driver only once something called
// dial. Each driver names its own field requirements via
// Driver.RequiredFields, so adding MySQL's (Host, User, ...) is implementing
// that method in the mysql package, not editing a condition here.
//
// Coordinator-flagged: this used to leave an unregistered driver id alone,
// on the theory that dial's own KindUnsupported check already reports it and
// duplicating that check could only disagree with it. That argument missed
// the actual complaint — the user reported, in person, "it saved a
// connection that can never work". dial does still check the driver id
// (session.go and connections.test's own call to dial both still need
// that — the engine is a separate process with its own JSON-RPC API, and
// connections.save is not dial's only caller), but by the time dial ever
// runs, checkConnectionIsSavable's whole point is to have already stopped
// the record from being committed in the first place. Reporting the same
// problem twice is fine; reporting it only after the damage is done is not.
func checkConnectionIsSavable(rec store.Saved) error {
	d, ok := driver.Lookup(rec.Driver)
	if !ok {
		return dberr.New(dberr.KindInvalid, "no driver named "+rec.Driver)
	}
	missing := d.RequiredFields(rec.ConnConfig(""))
	if len(missing) == 0 {
		return nil
	}
	labels := make([]string, len(missing))
	for i, field := range missing {
		labels[i] = strings.ToUpper(field[:1]) + field[1:]
	}
	return dberr.New(dberr.KindInvalid, strings.Join(labels, ", ")+" is required")
}

// dial resolves the driver and opens a connection, returning the resolved
// Driver alongside the Conn. Callers that need driver-level information
// after a successful dial (session.open's Capabilities, for one) use the
// value handed back here instead of looking the driver up a second time:
// with only one lookup, there is no way for the two to disagree.
func dial(ctx context.Context, rec store.Saved, password string) (driver.Conn, driver.Driver, error) {
	d, ok := driver.Lookup(rec.Driver)
	if !ok {
		return nil, nil, dberr.New(dberr.KindUnsupported, "no driver named "+rec.Driver)
	}
	conn, err := d.Open(ctx, rec.ConnConfig(password))
	if err != nil {
		return nil, nil, err
	}
	return conn, d, nil
}
