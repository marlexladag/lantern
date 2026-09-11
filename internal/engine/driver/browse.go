package driver

import (
	"context"
	"encoding/json"

	"github.com/marlexladag/lantern/internal/engine/dberr"
)

// MaxBrowseLimit caps a single page. An unbounded limit is how a UI bug
// becomes an out-of-memory crash on a table with a hundred million rows.
const MaxBrowseLimit = 1000

// SortKey is one ORDER BY term.
type SortKey struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc"`
}

// BrowseRequest asks for one page of a table, semantically. The caller never
// writes SQL: each driver renders this in its own dialect, which is what
// makes a second engine an added implementation rather than a rewrite.
//
// Browsing is stateless by design: there is no server-side cursor to hold
// open or leak when a UI tab closes. Each request is self-contained, carrying
// whatever the previous page's Keyset handed back in After. That is narrower
// than spec section 7's windowed cursor, which exists for arbitrary SQL — a
// result set that cannot be re-derived and must therefore be held open. A
// table can always be re-derived from its BrowseRequest, so it never needs
// that cursor's cost.
type BrowseRequest struct {
	Database string `json:"database"`
	Table    string `json:"table"`
	// Sort is empty for the driver's stable default, which should be the
	// primary key so that pagination is deterministic. A request may carry a
	// non-empty After alongside an empty Sort: that is the ordinary shape of
	// a second page when the caller never chose a sort, not a contradiction
	// — the driver applies the same default order both times, and the caller
	// is never expected to know what that default is, only to echo back the
	// Keyset it was handed.
	Sort []SortKey `json:"sort,omitempty"`
	// After is the previous page's Keyset, echoed back untouched. Nil for the
	// first page.
	After []Value `json:"after,omitempty"`
	// Offset is used only when the driver told the caller it could not
	// paginate by key.
	Offset int `json:"offset,omitempty"`
	Limit  int `json:"limit"`
}

// Validate rejects a request no driver should be asked to run.
func (r BrowseRequest) Validate() error {
	if r.Table == "" {
		return dberr.New(dberr.KindInvalid, "browse: table is required")
	}
	if r.Limit <= 0 {
		return dberr.New(dberr.KindInvalid, "browse: limit must be positive")
	}
	if r.Limit > MaxBrowseLimit {
		return dberr.New(dberr.KindInvalid, "browse: limit exceeds the maximum page size")
	}
	return nil
}

// BrowsePage is one page of rows.
type BrowsePage struct {
	Columns []ColumnMeta `json:"columns"`
	Rows    [][]Value    `json:"rows"`
	// Keyset carries whatever the driver needs to fetch the next page, taken
	// from the last row. The caller echoes it back in After without
	// understanding it. Absent when the driver paginated by offset, or when
	// the result is exhausted.
	Keyset []Value `json:"keyset,omitempty"`
	// Exhausted is true when fewer rows than Limit came back, so the caller
	// can stop asking.
	Exhausted bool `json:"exhausted"`
	// Offset is the offset of the row after this page, for drivers that could
	// not paginate by key.
	Offset int `json:"offset"`
}

// MarshalJSON guarantees Rows serializes as [] rather than null. Rows is a
// plain slice with no omitempty, so its zero value — nil, exactly what an
// empty table or an exhausted After produces — would otherwise marshal to
// the JSON literal null through encoding/json's default behavior. The
// TypeScript side declares Rows as an array; null landing where an array is
// expected doesn't fail loudly on this one field, it has blanked the whole
// app before. Guaranteed once here rather than trusted to every driver that
// builds a page.
func (p BrowsePage) MarshalJSON() ([]byte, error) {
	type alias BrowsePage
	a := alias(p)
	if a.Rows == nil {
		a.Rows = [][]Value{}
	}
	return json.Marshal(a)
}

// Browser is an OPTIONAL interface. A driver implements it when it can page a
// table; the caller discovers it by type assertion, exactly as spec section 4
// requires, so a schemaless engine is never forced to stub it.
type Browser interface {
	Browse(ctx context.Context, req BrowseRequest) (*BrowsePage, error)
}
