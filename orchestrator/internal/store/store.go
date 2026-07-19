// Package store is the control plane's source of truth. It holds three related
// records:
//
//	Project   — a logical grouping / tenant (billing + ownership live here)
//	Workload  — the routable, independently-scheduled, scale-to-zero unit
//	Route     — a hostname that resolves to a workload (many routes → one workload)
//
// One project owns many workloads; each workload has one or more routes. A
// plain tinbase project is just a project with a single workload and one route.
// v0 persists to a JSON file; this is the seam where managed Postgres slots in.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
)

var ErrNotFound = errors.New("not found")

// Project is a logical grouping of workloads under one tenant. Its ID is a
// DNS-label-safe short ref used as the subdomain prefix for its workloads.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Region    string    `json:"region"`
	CreatedAt time.Time `json:"created_at"`
}

// Workload is a single routable instance. It is what the runtime driver operates
// on (keyed by ID), and where per-instance credentials + lifecycle state live.
type Workload struct {
	ID        string               `json:"id"`
	ProjectID string               `json:"project_id"`
	Type      runtime.WorkloadType `json:"type"`
	Name      string               `json:"name"` // role within the project ("", "api", "web", ...)
	Image     string               `json:"image,omitempty"`
	Port      int                  `json:"port,omitempty"`
	MemoryMB  int                  `json:"memory_mb,omitempty"`
	CPUs      float64              `json:"cpus,omitempty"`
	State     runtime.State        `json:"state"`
	DataDir   string               `json:"data_dir"`
	JWTSecret string               `json:"jwt_secret,omitempty"`
	AnonKey   string               `json:"anon_key,omitempty"`
	SvcKey    string               `json:"service_role_key,omitempty"`
	CreatedAt time.Time            `json:"created_at"`
}

// Route maps a workload to a hostname and a stable key. Host drives subdomain
// routing (<key>.<base>); Key drives subroute routing (/w/<key>) until wildcard
// subdomains are wired. Host is stored lowercased, without a port.
type Route struct {
	Host       string    `json:"host"`
	Key        string    `json:"key"`
	WorkloadID string    `json:"workload_id"`
	CreatedAt  time.Time `json:"created_at"`
}

type Store struct {
	path      string
	mu        sync.RWMutex
	projects  map[string]*Project
	workloads map[string]*Workload
	routes    map[string]*Route // key: lowercased host
}

type snapshot struct {
	Projects  []*Project  `json:"projects"`
	Workloads []*Workload `json:"workloads"`
	Routes    []*Route    `json:"routes"`
}

// Open loads the store from path, creating an empty one if it does not exist.
func Open(path string) (*Store, error) {
	s := &Store{
		path:      path,
		projects:  make(map[string]*Project),
		workloads: make(map[string]*Workload),
		routes:    make(map[string]*Route),
	}
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
	if len(b) > 0 {
		var snap snapshot
		if err := json.Unmarshal(b, &snap); err != nil {
			return nil, err
		}
		for _, p := range snap.Projects {
			s.projects[p.ID] = p
		}
		for _, w := range snap.Workloads {
			s.workloads[w.ID] = w
		}
		for _, r := range snap.Routes {
			s.routes[strings.ToLower(r.Host)] = r
		}
	}
	return s, nil
}

// ---- Projects ----

func (s *Store) PutProject(p *Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	s.projects[p.ID] = &cp
	return s.flushLocked()
}

func (s *Store) GetProject(id string) (*Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (s *Store) ListProjects() []*Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Project, 0, len(s.projects))
	for _, p := range s.projects {
		cp := *p
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// DeleteProject removes a project and cascades to its workloads and their
// routes. Callers must stop the running instances first.
func (s *Store) DeleteProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[id]; !ok {
		return ErrNotFound
	}
	delete(s.projects, id)
	for wid, w := range s.workloads {
		if w.ProjectID == id {
			delete(s.workloads, wid)
			s.deleteRoutesForWorkloadLocked(wid)
		}
	}
	return s.flushLocked()
}

// ---- Workloads ----

func (s *Store) PutWorkload(w *Workload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *w
	s.workloads[w.ID] = &cp
	return s.flushLocked()
}

func (s *Store) GetWorkload(id string) (*Workload, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.workloads[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *w
	return &cp, nil
}

// ListWorkloads returns the workloads for a project (all workloads if projectID
// is empty).
func (s *Store) ListWorkloads(projectID string) []*Workload {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Workload, 0)
	for _, w := range s.workloads {
		if projectID == "" || w.ProjectID == projectID {
			cp := *w
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *Store) SetWorkloadState(id string, state runtime.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.workloads[id]
	if !ok {
		return ErrNotFound
	}
	w.State = state
	return s.flushLocked()
}

// DeleteWorkload removes a workload and its routes. Caller stops the instance.
func (s *Store) DeleteWorkload(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workloads[id]; !ok {
		return ErrNotFound
	}
	delete(s.workloads, id)
	s.deleteRoutesForWorkloadLocked(id)
	return s.flushLocked()
}

// ---- Routes ----

func (s *Store) PutRoute(r *Route) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *r
	cp.Host = strings.ToLower(cp.Host)
	s.routes[cp.Host] = &cp
	return s.flushLocked()
}

// GetRouteByHost resolves a hostname (case-insensitive) to its route.
func (s *Store) GetRouteByHost(host string) (*Route, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.routes[strings.ToLower(host)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *r
	return &cp, nil
}

// GetRouteByKey resolves a subroute key to its route.
func (s *Store) GetRouteByKey(key string) (*Route, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key = strings.ToLower(key)
	for _, r := range s.routes {
		if strings.ToLower(r.Key) == key {
			cp := *r
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (s *Store) ListRoutesForWorkload(workloadID string) []*Route {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Route, 0)
	for _, r := range s.routes {
		if r.WorkloadID == workloadID {
			cp := *r
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

func (s *Store) deleteRoutesForWorkloadLocked(workloadID string) {
	for host, r := range s.routes {
		if r.WorkloadID == workloadID {
			delete(s.routes, host)
		}
	}
}

// flushLocked writes the whole store atomically. Caller must hold s.mu.
func (s *Store) flushLocked() error {
	snap := snapshot{}
	for _, p := range s.projects {
		snap.Projects = append(snap.Projects, p)
	}
	for _, w := range s.workloads {
		snap.Workloads = append(snap.Workloads, w)
	}
	for _, r := range s.routes {
		snap.Routes = append(snap.Routes, r)
	}
	sort.Slice(snap.Projects, func(i, j int) bool { return snap.Projects[i].CreatedAt.Before(snap.Projects[j].CreatedAt) })
	sort.Slice(snap.Workloads, func(i, j int) bool { return snap.Workloads[i].CreatedAt.Before(snap.Workloads[j].CreatedAt) })
	sort.Slice(snap.Routes, func(i, j int) bool { return snap.Routes[i].Host < snap.Routes[j].Host })

	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
