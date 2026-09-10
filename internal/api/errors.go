// Package api exposes the engine over JSON-RPC. It is the only package that
// knows about both the engine and the transport; internal/engine/... must
// never import it.
package api

import (
	"encoding/json"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/rpc"
)

// ToRPCError turns any engine error into a JSON-RPC error carrying the full
// normalized error in its data member. The code is always CodeDatabase; the
// UI branches on the Kind inside data, not on the number (spec section 11).
func ToRPCError(err error) error {
	if err == nil {
		return nil
	}
	e := dberr.From(err)
	// json.Marshal cannot fail here: dberr.Error's fields are exclusively
	// Kind (a defined string type) and plain strings, none of which
	// encoding/json's Marshal ever rejects (it only fails on NaN/Inf floats,
	// a cycle, or a chan/func/complex value — see encode.go). e is also
	// never nil: dberr.From only returns nil when err is nil, which is ruled
	// out above. There is deliberately no error check on this Marshal —
	// one would be untestable dead code, the same reasoning store.go's
	// writeLocked drops the json.MarshalIndent error check under.
	data, _ := json.Marshal(e)
	return &rpc.Error{Code: rpc.CodeDatabase, Message: e.Message, Data: data}
}
