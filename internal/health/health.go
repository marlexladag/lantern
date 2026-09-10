// Package health implements the engine's liveness method. The shell calls it
// as a startup handshake before showing the main window, and periodically
// afterwards to detect a hung engine.
package health

import (
	"context"
	"encoding/json"
	"os"

	"github.com/marlexladag/lantern/internal/rpc"
)

// Info is the health method's result.
type Info struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	PID     int    `json:"pid"`
}

// Handler returns the health method, closing over the build metadata. It
// ignores its params so that callers may send none.
func Handler(version, commit string) rpc.Handler {
	return func(context.Context, json.RawMessage) (any, error) {
		return Info{
			Status:  "ok",
			Version: version,
			Commit:  commit,
			PID:     os.Getpid(),
		}, nil
	}
}
