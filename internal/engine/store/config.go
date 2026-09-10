// Package store persists connection settings.
//
// Spec section 6 is strict: configuration goes to a JSON file, passwords go to
// the OS keychain, and the two never mix. The Saved struct has no password
// field at all, so writing one to disk is impossible rather than merely
// discouraged.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/marlexladag/lantern/internal/engine/dberr"
	"github.com/marlexladag/lantern/internal/engine/driver"
)

// Saved is one stored connection. It deliberately has no password field.
type Saved struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Database string `json:"database,omitempty"`
	File     string `json:"file,omitempty"`
	// Color tints the window and tabs. It is a safety feature, not decoration
	// (spec section 12): production is red.
	Color    string `json:"color"`
	ReadOnly bool   `json:"read_only"`
}

// ConnConfig turns a stored record plus a runtime password into a dial-ready
// config. The password is supplied by the caller; it is never held here.
func (s Saved) ConnConfig(password string) driver.ConnConfig {
	return driver.ConnConfig{
		Driver:   s.Driver,
		Host:     s.Host,
		Port:     s.Port,
		User:     s.User,
		Password: password,
		Database: s.Database,
		File:     s.File,
	}
}

// configDirEnv, when set, overrides the directory DefaultPath resolves
// connections.json under, in place of os.UserConfigDir. It exists so a
// shell session experimenting with a real engine binary — piping JSON-RPC
// at it by hand, the way this repo's own task reports do — can point it at
// a scratch directory instead of risking whatever is already saved for the
// real user. os.UserConfigDir alone is not a safe thing to rely on here: on
// macOS it ignores XDG_CONFIG_HOME entirely and always resolves to
// $HOME/Library/Application Support, so setting that variable does *not*
// redirect it.
const configDirEnv = "LANTERN_CONFIG_DIR"

// DefaultPath is where connections live on this machine: LANTERN_CONFIG_DIR
// if set (see configDirEnv), otherwise the OS-specific user config
// directory from os.UserConfigDir. Either way the layout underneath is the
// same: a "lantern/connections.json" suffix.
func DefaultPath() (string, error) {
	dir := os.Getenv(configDirEnv)
	if dir == "" {
		var err error
		dir, err = os.UserConfigDir()
		if err != nil {
			return "", dberr.Wrap(dberr.KindUnknown, "cannot locate the user config directory", err)
		}
	}
	return filepath.Join(dir, "lantern", "connections.json"), nil
}

// Store reads and writes the connection list.
type Store struct {
	mu      sync.Mutex
	path    string
	keyring Keyring
}

// New returns a store backed by path, with secrets in kr.
func New(path string, kr Keyring) *Store {
	return &Store{path: path, keyring: kr}
}

// List returns every stored connection. A missing file is an empty list, not
// an error — a first run is not a failure.
func (s *Store) List() ([]Saved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *Store) readLocked() ([]Saved, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Saved{}, nil
	}
	if err != nil {
		return nil, dberr.Wrap(dberr.KindUnknown, "cannot read the connection file", err)
	}
	var out []Saved
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, dberr.Wrap(dberr.KindUnknown, "the connection file is not valid JSON", err)
	}
	if out == nil {
		out = []Saved{}
	}
	return out, nil
}

func (s *Store) writeLocked(records []Saved) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return dberr.Wrap(dberr.KindUnknown, "cannot create the config directory", err)
	}
	// json.MarshalIndent cannot fail for a []Saved, on any Go version: per
	// encoding/json's own source (encode.go), Marshal only ever fails with
	// UnsupportedValueError (NaN/Inf floats, or a cycle reachable through a
	// pointer/interface/map) or UnsupportedTypeError (chan, func, complex).
	// Saved's fields are exclusively string, int and bool — none of those
	// categories — so unlike newID's crypto/rand.Read check below, this
	// isn't a version-dependent guarantee, it's unconditional. There is
	// deliberately no error check here — one would be untestable dead code,
	// the same reasoning the sqlite driver's classify nil-guard was removed
	// under.
	raw, _ := json.MarshalIndent(records, "", "  ")
	// Write to a sibling and rename, so an interrupted write cannot leave a
	// half-written connection list behind. Clean up the temp file on every
	// failure path from here on, including a write that fails partway
	// through (e.g. disk full) and leaves a partial file behind.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		_ = os.Remove(tmp)
		return dberr.Wrap(dberr.KindUnknown, "cannot write the connection file", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return dberr.Wrap(dberr.KindUnknown, "cannot replace the connection file", err)
	}
	return nil
}

// Save stores a connection, assigning an ID when it has none, and files the
// password in the keychain. An empty password stores nothing.
func (s *Store) Save(c Saved, password string) (Saved, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.readLocked()
	if err != nil {
		return Saved{}, err
	}

	if c.ID == "" {
		c.ID = newID()
		records = append(records, c)
	} else {
		found := false
		for i := range records {
			if records[i].ID == c.ID {
				records[i] = c
				found = true
				break
			}
		}
		if !found {
			records = append(records, c)
		}
	}

	secretStored := false
	// priorKnown/priorExists/priorSecret capture whatever was filed under
	// this id before Set below (possibly) overwrites it, so a config-write
	// failure can restore exactly that state rather than blindly deleting
	// whatever Set just wrote. On an update that changes the password, a
	// blind delete would destroy the connection's still-valid existing
	// secret, which is strictly worse than the drift this rollback exists
	// to prevent.
	var (
		priorKnown  bool
		priorExists bool
		priorSecret string
	)
	if password != "" {
		switch prior, err := s.keyring.Get(keyringService, c.ID); {
		case err == nil:
			priorKnown, priorExists, priorSecret = true, true, prior
		case errors.Is(err, ErrSecretNotFound):
			priorKnown, priorExists = true, false
		default:
			// Couldn't determine what, if anything, was there before —
			// priorKnown stays false, and the rollback below leaves the
			// keychain alone rather than guess.
		}

		if err := s.keyring.Set(keyringService, c.ID, password); err != nil {
			return Saved{}, dberr.Wrap(dberr.KindUnknown, "cannot store the password in the keychain", err)
		}
		secretStored = true
	}
	if err := s.writeLocked(records); err != nil {
		if secretStored {
			// The keychain write already succeeded but the config write
			// didn't, so without this the two stores would drift apart.
			// Ignore any failure from the rollback itself — we're already
			// returning the original write error, and a failed rollback
			// must not mask it.
			switch {
			case priorKnown && priorExists:
				// Restore the secret that was there before this Save call,
				// so the keychain and config file end up agreeing in both
				// directions (the update the user asked for didn't happen,
				// so neither should the password change).
				_ = s.keyring.Set(keyringService, c.ID, priorSecret)
			case priorKnown && !priorExists:
				// Nothing was filed under this id before — undo the Set by
				// removing what it just wrote.
				_ = s.keyring.Delete(keyringService, c.ID)
			default:
				// Couldn't prove what belonged there before Set ran.
				// Losing a secret is worse than leaving an extra one
				// behind, so leave the keychain exactly as Set left it.
			}
		}
		return Saved{}, err
	}
	return c, nil
}

// Delete removes a connection and its stored password.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.readLocked()
	if err != nil {
		return err
	}
	out := records[:0]
	found := false
	for _, r := range records {
		if r.ID == id {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return dberr.New(dberr.KindNotFound, "no connection with id "+id)
	}
	// A deleted connection must not leave its password in the keychain. A
	// missing secret is fine — not every connection has one.
	if err := s.keyring.Delete(keyringService, id); err != nil && !errors.Is(err, ErrSecretNotFound) {
		return dberr.Wrap(dberr.KindUnknown, "cannot remove the password from the keychain", err)
	}
	return s.writeLocked(out)
}

// Password returns the stored password, or an empty string when there is none.
func (s *Store) Password(id string) (string, error) {
	v, err := s.keyring.Get(keyringService, id)
	if errors.Is(err, ErrSecretNotFound) {
		return "", nil
	}
	if err != nil {
		return "", dberr.Wrap(dberr.KindAuth, "cannot read the password from the keychain", err)
	}
	return v, nil
}

func newID() string {
	b := make([]byte, 8)
	// As of Go 1.24 (see the crypto/rand.Read doc comment, and the release
	// notes for https://go.dev/issue/66821), Read is guaranteed never to
	// return an error: it crashes the program irrecoverably instead if the
	// OS entropy source ever fails. go.mod's floor is 1.24 specifically so
	// this guarantee holds for every build of this module — under Go 1.23,
	// Read could still return an error here, which this function would then
	// have silently ignored. There is deliberately no error branch to check
	// on the declared floor: one would be untestable dead code, the same
	// reasoning writeLocked's json.MarshalIndent error check was removed
	// under.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
