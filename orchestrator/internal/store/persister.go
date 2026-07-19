package store

import (
	"os"
	"path/filepath"
)

// Persister loads and saves the whole state snapshot as bytes. The store keeps
// state in memory and delegates durability to a Persister, so swapping a JSON
// file for Postgres (or nothing) is a one-line change — the seam for moving the
// control-plane index off a single box.
type Persister interface {
	Load() ([]byte, error) // nil, nil when nothing has been stored yet
	Save([]byte) error
}

// MemPersister persists nothing (in-memory store, for tests / ephemeral planes).
type MemPersister struct{}

func (MemPersister) Load() ([]byte, error) { return nil, nil }
func (MemPersister) Save([]byte) error     { return nil }

// FilePersister persists to a JSON file with an atomic write.
type FilePersister struct{ Path string }

func (f FilePersister) Load() ([]byte, error) {
	b, err := os.ReadFile(f.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}

func (f FilePersister) Save(b []byte) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}
