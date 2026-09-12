package sqlite

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/marlexladag/lantern/internal/engine/driver"
	"github.com/marlexladag/lantern/internal/engine/driver/drivertest"
)

// The shared suite, from the SQLite side. Spec section 13: this is the
// primary mechanism keeping many drivers honest, and it is the thing MySQL
// inherits instead of re-asserting from scratch.
//
// It does not replace this package's own tests and cannot: coverage is
// measured per package with no -coverpkg, so a suite living in drivertest
// contributes no coverage here. What it adds is the guarantee that whatever
// browse.go does, it does for reasons a second driver will be held to as
// well.
func TestConformance(t *testing.T) {
	// Which pagination each continuation took. The suite follows whichever
	// the driver chose, so an assertion inside it could only ever check
	// consistency — whether THIS driver reaches both paths is a question only
	// this package can ask. Written from the suite's own goroutine (t.Run
	// subtests are sequential here, nothing calls Parallel) and read after
	// Run returns.
	paths := map[drivertest.PagingPath]int{}

	drivertest.Run(t, drivertest.Config{
		Driver: New(),
		Open: func(drivertest.TestingT) driver.ConnConfig {
			// Open refuses a file that does not exist rather than silently
			// creating one, so the suite is handed a real, empty database.
			path := filepath.Join(t.TempDir(), "conformance.db")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatalf("seed an empty database file: %v", err)
			}
			return driver.ConnConfig{Driver: "sqlite", File: path}
		},
		Observe: func(p drivertest.PagingPath) { paths[p]++ },
	})

	// Both paths, asserted rather than assumed. Until the suite seeded a
	// view, nothing in its fixtures was unpagable by key, so this driver
	// never took its own offset fallback through the suite and the suite's
	// offset branch was dead against it — every check green, half the code
	// untouched. A fixture change that made the view pagable again, or a
	// planOrder change that stopped falling back, would be silent without
	// this.
	for _, want := range []drivertest.PagingPath{drivertest.PathKeyset, drivertest.PathOffset} {
		if paths[want] == 0 {
			t.Errorf("no page in the whole suite was continued by %s paging (counts: %v); "+
				"that path is untested against this driver", want, paths)
		}
	}
}
