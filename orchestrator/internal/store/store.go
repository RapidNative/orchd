// Package store is the control plane's source of truth: the durable record of
// every project (its routing ref, keys, JWT secret, data dir, region, and
// lifecycle state). v0 persists to a JSON file; this is the seam where a real
// deployment swaps in managed Postgres without touching callers.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
)

var ErrNotFound = errors.New("project not found")

// Project is a tenant record. It intentionally mirrors the fields the gateway and
// runtime need, plus the credentials tinbase mints so callers can point
// supabase-js at <ref>.tinbase.cloud immediately.
type Project struct {
	Ref       string               `json:"ref"`
	Type      runtime.WorkloadType `json:"type"`
	Region    string               `json:"region"`
	State     runtime.State        `json:"state"`
	DataDir   string               `json:"data_dir"`
	WorkDir   string               `json:"work_dir,omitempty"`
	JWTSecret string               `json:"jwt_secret"`
	AnonKey   string               `json:"anon_key,omitempty"`
	SvcKey    string               `json:"service_role_key,omitempty"`
	CreatedAt time.Time            `json:"created_at"`
}

// Store is a concurrency-safe, file-backed collection of projects.
type Store struct {
	path string
	mu   sync.RWMutex
	data map[string]*Project
}

// Open loads the store from path, creating an empty one if it does not exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path, data: make(map[string]*Project)}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Project
	if len(b) > 0 {
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, err
		}
	}
	for _, p := range list {
		s.data[p.Ref] = p
	}
	return s, nil
}

func (s *Store) Get(ref string) (*Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.data[ref]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (s *Store) List() []*Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Project, 0, len(s.data))
	for _, p := range s.data {
		cp := *p
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Put inserts or replaces a project and flushes to disk.
func (s *Store) Put(p *Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	s.data[p.Ref] = &cp
	return s.flushLocked()
}

// SetState updates only the lifecycle state; no-op if the project is gone.
func (s *Store) SetState(ref string, state runtime.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.data[ref]
	if !ok {
		return ErrNotFound
	}
	p.State = state
	return s.flushLocked()
}

// Delete removes a project record (not its data dir; the caller owns cleanup).
func (s *Store) Delete(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[ref]; !ok {
		return ErrNotFound
	}
	delete(s.data, ref)
	return s.flushLocked()
}

// flushLocked writes the whole store atomically. Caller must hold s.mu.
func (s *Store) flushLocked() error {
	list := make([]*Project, 0, len(s.data))
	for _, p := range s.data {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
