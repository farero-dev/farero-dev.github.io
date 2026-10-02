// Package secret stores tokens and the gateway secret (F-10).
//
// The release build uses the macOS Keychain through the Security framework
// (keychain_darwin.go). zalando/go-keyring was ruled out in M0: it writes
// items through /usr/bin/security, so any process can read them back with
// `security find-generic-password -w` without a prompt (Q56).
//
// FileStore is for development and tests only. With ad-hoc signing every
// rebuild changes farerod's code signature and the Keychain would prompt.
package secret

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// ErrNotFound is returned when a key has no value.
var ErrNotFound = errors.New("secret not found")

// Store is a small key/value store for secrets.
type Store interface {
	Get(key string) (string, error)
	Set(key, value string) error
	Delete(key string) error
}

// Keys used by farerod.
const (
	KeyGatewaySecret = "gateway.secret"
)

// PluginKey is the key holding a plugin's credentials.
func PluginKey(plugin, what string) string { return "plugin." + plugin + "." + what }

// RandomToken returns a 256-bit hex token.
func RandomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// GetOrCreate returns key's value, generating and storing one if missing.
func GetOrCreate(s Store, key string, gen func() string) (string, error) {
	v, err := s.Get(key)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	v = gen()
	return v, s.Set(key, v)
}

// FileStore keeps secrets in a 0600 JSON file. Development only.
type FileStore struct {
	mu   sync.Mutex
	path string
}

// NewFileStore returns a file-backed store.
func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

func (f *FileStore) load() (map[string]string, error) {
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	return m, json.Unmarshal(b, &m)
}

func (f *FileStore) save(m map[string]string) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

// Get implements Store.
func (f *FileStore) Get(key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", err
	}
	v, ok := m[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// Set implements Store.
func (f *FileStore) Set(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[key] = value
	return f.save(m)
}

// Delete implements Store.
func (f *FileStore) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	delete(m, key)
	return f.save(m)
}

// MemoryStore is an in-memory Store for tests.
type MemoryStore struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{m: map[string]string{}} }

// Get implements Store.
func (s *MemoryStore) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// Set implements Store.
func (s *MemoryStore) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
	return nil
}

// Delete implements Store.
func (s *MemoryStore) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}
