package api

import (
	"context"
	"encoding/json"

	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/rpc"
)

// DriverInfo is what the shell needs to build a connection form without
// keeping its own copy of each driver's requirements. The duplication this
// replaces could only ever disagree: Go decides what a driver needs to dial,
// and a second list in TypeScript is a guess at that decision.
type DriverInfo struct {
	ID             string              `json:"id"`
	RequiredFields []string            `json:"required_fields"`
	Capabilities   driver.Capabilities `json:"capabilities"`
}

// RegisterDrivers wires drivers.list onto srv.
//
// It takes no parameters and reads no state outside the registry, so it needs
// neither a store nor a session table — a driver is registered at link time by
// its own init function, and this reports what that produced.
func RegisterDrivers(srv *rpc.Server) {
	srv.Register("drivers.list", func(ctx context.Context, _ json.RawMessage) (any, error) {
		ids := driver.IDs()
		// Never nil, for the same reason each entry's RequiredFields is
		// never nil: the shell iterates this, and a nil slice is the JSON
		// literal null.
		infos := make([]DriverInfo, 0, len(ids))
		for _, id := range ids {
			// The lookup cannot miss: id came from the registry an instant
			// ago, and the registry only ever grows — Register is its one
			// exported mutator and there is no Unregister. A not-found
			// branch here would be untestable dead code under the repo's
			// coverage gate, the same reasoning ToRPCError documents for
			// its own guaranteed-non-nil precondition.
			d, _ := driver.Lookup(id)
			// Asked with a zero ConnConfig, which is what "what does this
			// driver need before it has anything" means — the question a
			// blank form is asking. RequiredFields takes a config because a
			// driver's requirements may narrow once something is filled in
			// (SQLite's own returns nothing once File is set), so this is
			// the widest answer, not the only one: connections.save asks
			// again with the real config and stays the authority. A driver
			// whose requirements change shape mid-form — "user, unless a
			// socket is given" — would need to be re-asked per keystroke,
			// and nothing does that yet because nothing needs it.
			fields := d.RequiredFields(driver.ConnConfig{})
			if fields == nil {
				fields = []string{}
			}
			infos = append(infos, DriverInfo{
				ID:             id,
				RequiredFields: fields,
				Capabilities:   d.Capabilities(),
			})
		}
		return infos, nil
	})
}
