package api

import (
	"context"
	"encoding/json"

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
			return nil, ToRPCError(dberr.New(dberr.KindUnsupported, "a connection needs a name"))
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
		conn, err := dial(ctx, p.Connection, p.Password)
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

// dial resolves the driver and opens a connection.
func dial(ctx context.Context, rec store.Saved, password string) (driver.Conn, error) {
	d, ok := driver.Lookup(rec.Driver)
	if !ok {
		return nil, dberr.New(dberr.KindUnsupported, "no driver named "+rec.Driver)
	}
	return d.Open(ctx, rec.ConnConfig(password))
}
