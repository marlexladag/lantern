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
	})
}
