package driver

import (
	"os/exec"
	"strings"
	"testing"
)

// The engine layer must not import transport. Every dispatch brief for this
// project has told implementers "there is a layering check" — and until this
// file there was not one, only a comment in driver.go and the reviewer's
// goodwill. An architectural rule enforced by convention is a rule that holds
// until the first person who has not read the comment.
//
// The rule is directional, not cosmetic: internal/engine/... is the part of
// this codebase that a future in-process consumer (a CLI, a test harness, a
// different front end) could import without dragging in JSON-RPC, stdio
// framing, or a process supervisor. An import in this direction would not
// break a build — it would quietly weld the engine to one transport, and the
// cost would only show up the day something else wanted to use it.
var forbiddenByEngine = []string{
	"github.com/marlexladag/lantern/internal/rpc",
	"github.com/marlexladag/lantern/internal/api",
	"github.com/marlexladag/lantern/cmd/",
}

func TestEngineImportsNoTransport(t *testing.T) {
	// -deps so this catches an indirect import too: engine -> some helper ->
	// internal/rpc is exactly as welded as a direct one, and is the shape a
	// violation is most likely to actually take.
	out, err := exec.Command("go", "list", "-deps",
		"github.com/marlexladag/lantern/internal/engine/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}

	deps := strings.Fields(string(out))
	// A negative control. If the query ever returns nothing — a renamed
	// directory, a build failure swallowed by Output(), a module path change
	// — the loop below would pass while checking nothing at all.
	if len(deps) < 10 {
		t.Fatalf("go list returned %d packages; the query is broken, not the tree", len(deps))
	}

	for _, dep := range deps {
		for _, bad := range forbiddenByEngine {
			if strings.HasPrefix(dep, bad) {
				t.Errorf("internal/engine/... reaches %s, which is transport", dep)
			}
		}
	}
}
