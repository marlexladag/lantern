package store

// This file closes the gap between the brief's behavioural tests
// (store_test.go, left untouched) and the repo's 100% statement coverage
// gate (scripts/go-coverage.sh). It also carries the self-review checks the
// task asked for explicitly: atomic-write cleanup on every failure path,
// Delete succeeding when the keychain entry is already absent, Save on an
// unknown ID appending rather than silently dropping the record, and the
// config directory being created with restrictive permissions.
//
// None of these tests ever call OSKeyring()'s Get/Set/Delete against the
// real platform keychain. osKeyring's error mapping is exercised entirely
// through the keyringGet/keyringSet/keyringDelete package-level seam
// variables declared in secret.go, swapped out here and restored with
// t.Cleanup. Every other test here uses NewMemoryKeyring() or a small local
// fake that implements the Keyring interface in-process.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zalando/go-keyring"
)

// -- DefaultPath ----------------------------------------------------------

func TestDefaultPathIsUnderTheUserConfigDir(t *testing.T) {
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := filepath.Join("lantern", "connections.json")
	if filepath.Base(filepath.Dir(got)) != "lantern" || filepath.Base(got) != "connections.json" {
		t.Errorf("DefaultPath = %q, want a path ending in %q", got, want)
	}
}

// os.UserConfigDir fails when it cannot work out where that directory is:
// $HOME unset on Darwin, or neither $XDG_CONFIG_HOME nor $HOME set on other
// Unix. t.Setenv saves and restores both automatically, and forbids
// t.Parallel in this test, which is exactly the safety this needs since it
// touches process-global environment state.
func TestDefaultPathFailsWhenTheUserConfigDirCannotBeDetermined(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("UserConfigDir on windows depends on %AppData%, not HOME/XDG_CONFIG_HOME")
	}
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	if _, err := DefaultPath(); err == nil {
		t.Fatal("DefaultPath succeeded despite no locatable user config directory")
	}
}

// -- readLocked / List error paths ----------------------------------------

// A path with a non-directory component fails ReadFile with ENOTDIR, not
// ENOENT — a "can't read it" error distinct from "it doesn't exist yet",
// which is the branch TestListOnAMissingFileIsEmptyNotAnError (in
// store_test.go) does not reach.
func TestListFailsWhenTheConfigFileCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	path := filepath.Join(blocker, "connections.json")

	s := New(path, NewMemoryKeyring())
	if _, err := s.List(); err == nil {
		t.Fatal("List through a non-directory path component succeeded")
	}
}

func TestListFailsOnInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	if err := os.WriteFile(path, []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	s := New(path, NewMemoryKeyring())
	if _, err := s.List(); err == nil {
		t.Fatal("List over invalid JSON succeeded")
	}
}

// The JSON literal "null" unmarshals into a nil []Saved with no error; List
// must still hand back an empty, non-nil slice.
func TestListToleratesJSONNullAsAnEmptyList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	s := New(path, NewMemoryKeyring())
	got, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got == nil {
		t.Error("List returned a nil slice for a null config file, want an empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("got %d records, want 0", len(got))
	}
}

// -- writeLocked error paths (white-box: same package as config.go) -------

func TestWriteLockedFailsWhenTheConfigDirCannotBeCreated(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	path := filepath.Join(blocker, "sub", "connections.json")

	s := New(path, NewMemoryKeyring())
	if err := s.writeLocked([]Saved{{ID: "x", Name: "x", Driver: "sqlite"}}); err == nil {
		t.Fatal("writeLocked succeeded despite an uncreatable config directory")
	}
}

// A rename onto an existing directory fails; this is also the meaningful
// check for the self-review item "does the atomic write clean up its temp
// file on every failure path" — WriteFile has already created the temp file
// by this point, so the cleanup in the Rename failure branch is what's
// actually being exercised here.
func TestWriteLockedFailsWhenRenameFailsAndCleansUpTheTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connections.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("seed directory in place of the config file: %v", err)
	}

	s := New(path, NewMemoryKeyring())
	if err := s.writeLocked([]Saved{{ID: "x", Name: "x", Driver: "sqlite"}}); err == nil {
		t.Fatal("writeLocked succeeded despite the destination being a directory")
	}

	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp file was left behind after a failed write: stat err = %v", err)
	}
}

// Skip permission-based failure injection under a euid-0 process: root
// bypasses Unix permission bits entirely, so the write would simply
// succeed and the test would be asserting something false about this
// platform rather than about the code.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("permission-bit based failure injection does not apply on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission checks are bypassed")
	}
}

// -- Save error paths -------------------------------------------------------

func TestSaveFailsWhenTheExistingConfigCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	path := filepath.Join(blocker, "connections.json")

	s := New(path, NewMemoryKeyring())
	if _, err := s.Save(Saved{Name: "x", Driver: "sqlite"}, ""); err == nil {
		t.Fatal("Save succeeded despite an unreadable existing config file")
	}
}

// Self-review item: saving a record whose ID is set but unknown to the
// store must append it, not silently discard it.
func TestSaveWithAnUnknownIDAppendsRatherThanDroppingIt(t *testing.T) {
	s, _, _ := newStore(t)
	first, err := s.Save(Saved{Name: "local", Driver: "sqlite", File: "/tmp/a.db"}, "")
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	stray := Saved{ID: "not-a-real-id", Name: "stray", Driver: "sqlite", File: "/tmp/b.db"}
	if _, err := s.Save(stray, ""); err != nil {
		t.Fatalf("save with an unknown id: %v", err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d records, want 2 (the original plus the appended stray): %+v", len(list), list)
	}
	var sawFirst, sawStray bool
	for _, r := range list {
		if r.ID == first.ID {
			sawFirst = true
		}
		if r.ID == "not-a-real-id" {
			sawStray = true
		}
	}
	if !sawFirst || !sawStray {
		t.Errorf("list = %+v, want both the original and the stray record", list)
	}
}

func TestSaveFailsWhenTheConfigFileCannotBeWritten(t *testing.T) {
	skipIfRoot(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "connections.json")
	s := New(path, NewMemoryKeyring())

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod config dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if _, err := s.Save(Saved{Name: "x", Driver: "sqlite"}, ""); err == nil {
		t.Fatal("Save succeeded despite an unwritable config directory")
	}
}

// -- fake Keyring for exercising Store's error-wrapping around the keychain

// erroringKeyring is a Keyring test double whose methods return whatever
// error each field names, or the same non-error behaviour as
// NewMemoryKeyring's empty case otherwise. It exists purely to drive Store's
// own error-wrapping branches around a Keyring failure; it never touches any
// real secret storage.
type erroringKeyring struct {
	getErr    error
	setErr    error
	deleteErr error
}

func (k erroringKeyring) Get(string, string) (string, error) {
	if k.getErr != nil {
		return "", k.getErr
	}
	return "", ErrSecretNotFound
}

func (k erroringKeyring) Set(string, string, string) error { return k.setErr }

func (k erroringKeyring) Delete(string, string) error {
	if k.deleteErr != nil {
		return k.deleteErr
	}
	return nil
}

func TestSaveFailsWhenTheKeyringCannotStoreThePassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	s := New(path, erroringKeyring{setErr: errors.New("keychain unavailable")})

	if _, err := s.Save(Saved{Name: "prod", Driver: "mysql", User: "app"}, "hunter2"); err == nil {
		t.Fatal("Save succeeded despite the keyring refusing to store the password")
	}
}

// Self-review item: deleting a connection whose keychain entry is already
// absent must succeed, not error — not every saved connection has a
// password.
func TestDeleteSucceedsWhenNoSecretWasEverStored(t *testing.T) {
	s, _, kr := newStore(t)
	saved, err := s.Save(Saved{Name: "local", Driver: "sqlite", File: "/tmp/a.db"}, "")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	// Confirm the premise: no secret exists for this id.
	if _, err := kr.Get(keyringService, saved.ID); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("premise violated: expected no secret, got err = %v", err)
	}

	if err := s.Delete(saved.ID); err != nil {
		t.Fatalf("delete of a connection with no stored secret: %v", err)
	}
	list, _ := s.List()
	if len(list) != 0 {
		t.Errorf("got %d records after delete, want 0", len(list))
	}
}

// Every other Delete test in this package deletes the store's only record,
// so the "keep this one, it doesn't match" branch of Delete's filter loop
// never runs. Two records, deleting one, is what actually exercises it.
func TestDeleteRemovesOnlyTheMatchingRecord(t *testing.T) {
	s, _, _ := newStore(t)
	keep, err := s.Save(Saved{Name: "keep", Driver: "sqlite", File: "/tmp/keep.db"}, "")
	if err != nil {
		t.Fatalf("save keep: %v", err)
	}
	gone, err := s.Save(Saved{Name: "gone", Driver: "sqlite", File: "/tmp/gone.db"}, "")
	if err != nil {
		t.Fatalf("save gone: %v", err)
	}

	if err := s.Delete(gone.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != keep.ID {
		t.Fatalf("list = %+v, want only %+v", list, keep)
	}
}

func TestDeleteFailsWhenTheExistingConfigCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	path := filepath.Join(blocker, "connections.json")

	s := New(path, NewMemoryKeyring())
	if err := s.Delete("whatever"); err == nil {
		t.Fatal("Delete succeeded despite an unreadable existing config file")
	}
}

func TestDeleteFailsWhenTheKeyringCannotRemoveTheSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	kr := erroringKeyring{deleteErr: errors.New("keychain unavailable")}
	s := New(path, kr)

	// password is empty so Save never calls Set, which would also error
	// against this fake (setErr is unset here, so it wouldn't, but this
	// keeps the test focused on Delete's own keyring.Delete error path).
	saved, err := s.Save(Saved{Name: "prod", Driver: "mysql", User: "app"}, "")
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := s.Delete(saved.ID); err == nil {
		t.Fatal("Delete succeeded despite the keyring refusing to remove the secret")
	}
}

// -- Password error path ---------------------------------------------------

func TestPasswordFailsWhenTheKeyringErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.json")
	s := New(path, erroringKeyring{getErr: errors.New("keychain unavailable")})

	if _, err := s.Password("whatever"); err == nil {
		t.Fatal("Password succeeded despite the keyring erroring")
	}
}

// -- Restrictive permissions on the config directory -----------------------

// Self-review item: the config directory must be created with restrictive
// permissions. Unix permission semantics don't translate to Windows ACLs, so
// this check is skipped there.
func TestConfigDirIsCreatedWithRestrictivePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits don't apply on windows")
	}

	base := t.TempDir()
	path := filepath.Join(base, "nested", "connections.json")
	s := New(path, NewMemoryKeyring())

	if _, err := s.Save(Saved{Name: "x", Driver: "sqlite"}, ""); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("config dir permissions = %o, want no group/other access", perm)
	}
}

// -- osKeyring: covered entirely through the keyringGet/Set/Delete seam,
// -- never against the real platform keychain -------------------------------

func TestOSKeyringGetMapsNotFoundToErrSecretNotFound(t *testing.T) {
	orig := keyringGet
	t.Cleanup(func() { keyringGet = orig })
	keyringGet = func(string, string) (string, error) { return "", keyring.ErrNotFound }

	_, err := OSKeyring().Get("svc", "user")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("err = %v, want ErrSecretNotFound", err)
	}
}

func TestOSKeyringGetReturnsTheStoredValue(t *testing.T) {
	orig := keyringGet
	t.Cleanup(func() { keyringGet = orig })
	keyringGet = func(string, string) (string, error) { return "hunter2", nil }

	got, err := OSKeyring().Get("svc", "user")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("got %q, want hunter2", got)
	}
}

func TestOSKeyringGetPassesThroughOtherErrors(t *testing.T) {
	orig := keyringGet
	t.Cleanup(func() { keyringGet = orig })
	want := errors.New("keychain locked")
	keyringGet = func(string, string) (string, error) { return "", want }

	_, err := OSKeyring().Get("svc", "user")
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestOSKeyringSetCallsTheSeamWithItsArguments(t *testing.T) {
	orig := keyringSet
	t.Cleanup(func() { keyringSet = orig })
	var gotService, gotUser, gotSecret string
	keyringSet = func(service, user, secret string) error {
		gotService, gotUser, gotSecret = service, user, secret
		return nil
	}

	if err := OSKeyring().Set("svc", "user", "hunter2"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if gotService != "svc" || gotUser != "user" || gotSecret != "hunter2" {
		t.Errorf("seam saw (%q, %q, %q)", gotService, gotUser, gotSecret)
	}
}

func TestOSKeyringSetPassesThroughAnError(t *testing.T) {
	orig := keyringSet
	t.Cleanup(func() { keyringSet = orig })
	want := errors.New("keychain locked")
	keyringSet = func(string, string, string) error { return want }

	if err := OSKeyring().Set("svc", "user", "hunter2"); !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestOSKeyringDeleteMapsNotFoundToErrSecretNotFound(t *testing.T) {
	orig := keyringDelete
	t.Cleanup(func() { keyringDelete = orig })
	keyringDelete = func(string, string) error { return keyring.ErrNotFound }

	if err := OSKeyring().Delete("svc", "user"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("err = %v, want ErrSecretNotFound", err)
	}
}

func TestOSKeyringDeleteSucceeds(t *testing.T) {
	orig := keyringDelete
	t.Cleanup(func() { keyringDelete = orig })
	keyringDelete = func(string, string) error { return nil }

	if err := OSKeyring().Delete("svc", "user"); err != nil {
		t.Errorf("delete: %v", err)
	}
}

func TestOSKeyringDeletePassesThroughOtherErrors(t *testing.T) {
	orig := keyringDelete
	t.Cleanup(func() { keyringDelete = orig })
	want := errors.New("keychain locked")
	keyringDelete = func(string, string) error { return want }

	if err := OSKeyring().Delete("svc", "user"); !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}
