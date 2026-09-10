package store

import (
	"errors"
	"sync"

	"github.com/zalando/go-keyring"
)

// keyringService is the service name every Lantern secret is filed under.
const keyringService = "com.marlexladag.lantern"

// ErrSecretNotFound reports that no secret is stored for a key.
var ErrSecretNotFound = errors.New("secret not found")

// Keyring is the secret backend. It exists as an interface so tests never
// touch the developer's real keychain.
type Keyring interface {
	Get(service, user string) (string, error)
	Set(service, user, secret string) error
	Delete(service, user string) error
}

// keyringGet, keyringSet and keyringDelete are the seams osKeyring calls
// through instead of the go-keyring package functions directly. A test can
// substitute them (restoring with t.Cleanup) to exercise osKeyring's error
// mapping without ever touching the real platform keychain.
var (
	keyringGet    = keyring.Get
	keyringSet    = keyring.Set
	keyringDelete = keyring.Delete
)

// OSKeyring returns the platform keychain: Keychain on macOS, Credential
// Manager on Windows, Secret Service on Linux.
func OSKeyring() Keyring { return osKeyring{} }

type osKeyring struct{}

func (osKeyring) Get(service, user string) (string, error) {
	v, err := keyringGet(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrSecretNotFound
	}
	return v, err
}

func (osKeyring) Set(service, user, secret string) error {
	return keyringSet(service, user, secret)
}

func (osKeyring) Delete(service, user string) error {
	err := keyringDelete(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrSecretNotFound
	}
	return err
}

// NewMemoryKeyring returns an in-process keyring for tests.
func NewMemoryKeyring() Keyring {
	return &memoryKeyring{secrets: make(map[string]string)}
}

type memoryKeyring struct {
	mu      sync.Mutex
	secrets map[string]string
}

func (m *memoryKeyring) key(service, user string) string { return service + "\x00" + user }

func (m *memoryKeyring) Get(service, user string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.secrets[m.key(service, user)]
	if !ok {
		return "", ErrSecretNotFound
	}
	return v, nil
}

func (m *memoryKeyring) Set(service, user, secret string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secrets[m.key(service, user)] = secret
	return nil
}

func (m *memoryKeyring) Delete(service, user string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(service, user)
	if _, ok := m.secrets[k]; !ok {
		return ErrSecretNotFound
	}
	delete(m.secrets, k)
	return nil
}
