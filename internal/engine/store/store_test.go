package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) (*Store, string, Keyring) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "connections.json")
	kr := NewMemoryKeyring()
	return New(path, kr), path, kr
}

func TestListOnAMissingFileIsEmptyNotAnError(t *testing.T) {
	s, _, _ := newStore(t)
	got, err := s.List()
	if err != nil {
		t.Fatalf("List on a missing file: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d records, want 0", len(got))
	}
}

func TestSaveAssignsAnIDAndListReturnsIt(t *testing.T) {
	s, _, _ := newStore(t)

	saved, err := s.Save(Saved{Name: "local", Driver: "sqlite", File: "/tmp/a.db"}, "")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("Save did not assign an ID")
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "local" || list[0].ID != saved.ID {
		t.Fatalf("list = %+v", list)
	}
}

func TestSaveWithAnExistingIDUpdatesInPlace(t *testing.T) {
	s, _, _ := newStore(t)
	first, _ := s.Save(Saved{Name: "local", Driver: "sqlite", File: "/tmp/a.db"}, "")

	first.Name = "renamed"
	if _, err := s.Save(first, ""); err != nil {
		t.Fatalf("update: %v", err)
	}

	list, _ := s.List()
	if len(list) != 1 {
		t.Fatalf("got %d records after an update, want 1", len(list))
	}
	if list[0].Name != "renamed" {
		t.Errorf("name = %q, want renamed", list[0].Name)
	}
}

// The single most important test in this package.
func TestThePasswordNeverReachesTheConfigFile(t *testing.T) {
	s, path, _ := newStore(t)
	if _, err := s.Save(Saved{Name: "prod", Driver: "mysql", User: "app"}, "hunter2-super-secret"); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(raw), "hunter2-super-secret") {
		t.Fatalf("THE PASSWORD WAS WRITTEN TO DISK:\n%s", raw)
	}

	// And confirm the file is otherwise the record we expect, so this test
	// cannot pass simply because nothing was written at all.
	var records []Saved
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	if len(records) != 1 || records[0].User != "app" {
		t.Fatalf("records = %+v", records)
	}
}

func TestPasswordRoundTripsThroughTheKeyring(t *testing.T) {
	s, _, _ := newStore(t)
	saved, _ := s.Save(Saved{Name: "prod", Driver: "mysql", User: "app"}, "hunter2")

	got, err := s.Password(saved.ID)
	if err != nil {
		t.Fatalf("password: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("password = %q, want hunter2", got)
	}
}

func TestPasswordIsEmptyWhenNoneWasStored(t *testing.T) {
	s, _, _ := newStore(t)
	saved, _ := s.Save(Saved{Name: "local", Driver: "sqlite", File: "/tmp/a.db"}, "")

	got, err := s.Password(saved.ID)
	if err != nil {
		t.Fatalf("password: %v", err)
	}
	if got != "" {
		t.Errorf("password = %q, want empty", got)
	}
}

func TestDeleteRemovesTheRecordAndItsSecret(t *testing.T) {
	s, _, kr := newStore(t)
	saved, _ := s.Save(Saved{Name: "prod", Driver: "mysql", User: "app"}, "hunter2")

	if err := s.Delete(saved.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	list, _ := s.List()
	if len(list) != 0 {
		t.Errorf("got %d records after delete, want 0", len(list))
	}
	// A deleted connection must not leave its password behind in the keychain.
	if _, err := kr.Get(keyringService, saved.ID); err == nil {
		t.Error("the secret survived the connection being deleted")
	}
}

func TestDeleteOfAnUnknownIDIsAnError(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Delete("nope"); err == nil {
		t.Error("deleting an unknown id succeeded")
	}
}

func TestConnConfigCarriesThePasswordButSavedDoesNot(t *testing.T) {
	cfg := Saved{Driver: "sqlite", File: "/tmp/a.db"}.ConnConfig("secret")
	if cfg.Password != "secret" {
		t.Errorf("ConnConfig lost the password")
	}
	if cfg.File != "/tmp/a.db" || cfg.Driver != "sqlite" {
		t.Errorf("ConnConfig = %+v", cfg)
	}
}

// Coordinator-flagged: ReadOnly was persisted and drawn as a lock icon in
// the sidebar, but ConnConfig dropped it on the floor, so the flag never
// reached a driver to enforce. Both states are asserted, not just true: a
// ConnConfig that always reports ReadOnly (whatever Saved.ReadOnly said)
// would just as wrongly force every connection read-only.
func TestConnConfigCarriesTheReadOnlyFlag(t *testing.T) {
	ro := Saved{Driver: "sqlite", File: "/tmp/a.db", ReadOnly: true}.ConnConfig("")
	if !ro.ReadOnly {
		t.Error("ConnConfig lost ReadOnly=true")
	}

	rw := Saved{Driver: "sqlite", File: "/tmp/a.db", ReadOnly: false}.ConnConfig("")
	if rw.ReadOnly {
		t.Error("ConnConfig reported ReadOnly=true for a Saved with ReadOnly=false")
	}
}

func TestSavedHasNoPasswordField(t *testing.T) {
	b, err := json.Marshal(Saved{Name: "x", Driver: "sqlite"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"password", "secret", "pass"} {
		if strings.Contains(strings.ToLower(string(b)), forbidden) {
			t.Errorf("Saved serialises a %q-ish field: %s", forbidden, b)
		}
	}
}

func TestMemoryKeyringReportsAMissingSecret(t *testing.T) {
	kr := NewMemoryKeyring()
	if _, err := kr.Get("svc", "nobody"); err == nil {
		t.Error("Get on an absent secret succeeded")
	}
}
