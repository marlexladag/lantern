package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/rpc"
)

// browseParams is a BrowseRequest plus the session id naming which
// connection to browse against. driver.BrowseRequest is embedded, not
// copied out field by field, so every field it declares — SortToken most
// critically, since a request carrying After with no SortToken is refused
// by the driver rather than silently mispaging (see BrowseRequest's own
// doc comment) — crosses the wire under exactly the json tag
// BrowseRequest itself owns. A hand-copied struct here would only need one
// forgotten or mistyped field to silently drop it in one direction while
// every test built on Go values kept passing.
type browseParams struct {
	SessionID string `json:"session_id"`
	driver.BrowseRequest
}

// RegisterBrowse wires the stateless table-browsing method onto srv.
//
// There is deliberately no browse.close and no cursor registry: Task 2 made
// browsing stateless by design (see BrowseRequest's own doc comment) — each
// request is self-contained, carrying whatever the previous page handed
// back, so this method opens nothing server-side and therefore has nothing
// to leak when a UI tab closes, a window is closed, or the shell crashes.
func RegisterBrowse(srv *rpc.Server, sess *Sessions) {
	srv.Register("browse.page", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p browseParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "browse.page: "+err.Error())
		}
		conn, err := sess.get(p.SessionID)
		if err != nil {
			return nil, ToRPCError(err)
		}
		// Browser is optional (spec section 4): every driver implements it
		// today, but Redis and MongoDB, once added, will not be able to. A
		// failed type assertion must report a clear, named KindUnsupported
		// rather than let a nil *BrowsePage or a nil-method call panic the
		// dispatch goroutine. Sessions stores only a driver.Conn, with no
		// driver id alongside it, so %T — the connection's own concrete
		// type, e.g. *sqlite.conn — is what names the driver here.
		browser, ok := conn.(driver.Browser)
		if !ok {
			return nil, ToRPCError(dberr.New(dberr.KindUnsupported,
				fmt.Sprintf("%T does not support browsing tables", conn)))
		}
		page, err := browser.Browse(ctx, p.BrowseRequest)
		if err != nil {
			return nil, ToRPCError(err)
		}
		return page, nil
	})
}
