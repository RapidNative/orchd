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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
)

var ErrNotFound = errors.New("not found")

// Store is the control-plane state backend. memStore (JSON file, or in-memory
// when the path is empty) is the only implementation today; a SQLite/Postgres
// adaptor drops in behind this interface without touching the manager or API —
// the seam for moving state off a single box for a distributed control plane.
type Store interface {
	PutProject(*Project) error
	GetProject(id string) (*Project, error)
	ListProjects() []*Project
	DeleteProject(id string) error

	PutWorkload(*Workload) error
	GetWorkload(id string) (*Workload, error)
	ListWorkloads(projectID string) []*Workload
	SetWorkloadState(id string, state runtime.State) error
	DeleteWorkload(id string) error

	PutRoute(*Route) error
	GetRouteByHost(host string) (*Route, error)
	GetRouteByKey(key string) (*Route, error)
	DeleteRoute(host string) error
	ListRoutesForWorkload(workloadID string) []*Route

	PutRegion(*Region) error
	GetRegion(id string) (*Region, error)
	ListRegions() []*Region
	DeleteRegion(id string) error

	PutAPIKey(*APIKey) error
	ListAPIKeys() []*APIKey
	DeleteAPIKey(id string) error

	GetSettings() Settings
	SetSettings(Settings) error
}

// APIKey is a named control-plane credential with a role. Only the sha256 hash
// is stored; the plaintext key is shown once at creation and never again.
type APIKey struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Role      string    `json:"role"` // "admin" | "readonly"
	Hash      string    `json:"hash,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Region is a placement target. Today all regions run on the local box; the
// DockerHost field is the seam for pointing a region at a remote worker node
// (the DockerDriver already accepts a remote daemon), enabling multi-node later.
type Region struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	DockerHost string    `json:"docker_host,omitempty"`
	IsDefault  bool      `json:"is_default"`
	CreatedAt  time.Time `json:"created_at"`
}

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

// BackupTarget configures where backups are stored. Type "local" uses the
// on-box store; "s3" uses an S3-compatible object store (S3/R2/B2/MinIO). The
// secret is persisted like other secrets in this store (plaintext on disk) and
// masked by the API.
type BackupTarget struct {
	Type      string `json:"type"` // "local" | "s3"
	Endpoint  string `json:"endpoint,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Region    string `json:"region,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty"`
}

// Webhook forwards platform events to an external URL (empty = off).
type Webhook struct {
	URL string `json:"url,omitempty"`
}

// MetricsTarget selects where the platform metrics snapshot is published.
type MetricsTarget struct {
	Type string `json:"type"` // "nop" | "log" | "http"
	URL  string `json:"url,omitempty"`
}

// Settings is the mutable platform configuration set at runtime (via the admin).
type Settings struct {
	Backup  BackupTarget  `json:"backup"`
	Webhook Webhook       `json:"webhook"`
	Metrics MetricsTarget `json:"metrics"`
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

type memStore struct {
	persister Persister
	mu        sync.RWMutex
	projects  map[string]*Project
	workloads map[string]*Workload
	routes    map[string]*Route // key: lowercased host
	regions   map[string]*Region
	apikeys   map[string]*APIKey
	settings  Settings
}

type snapshot struct {
	Projects  []*Project  `json:"projects"`
	Workloads []*Workload `json:"workloads"`
	Routes    []*Route    `json:"routes"`
	Regions   []*Region   `json:"regions"`
	APIKeys   []*APIKey   `json:"api_keys"`
	Settings  Settings    `json:"settings"`
}

// Open returns a file-backed store (JSON at path), or an in-memory store when
// path is empty. OpenPostgres (pg.go) returns a Postgres-backed one.
func Open(path string) (Store, error) {
	if path == "" {
		return openWith(MemPersister{})
	}
	return openWith(FilePersister{Path: path})
}

func openWith(p Persister) (*memStore, error) {
	s := &memStore{
		persister: p,
		projects:  make(map[string]*Project),
		workloads: make(map[string]*Workload),
		routes:    make(map[string]*Route),
		regions:   make(map[string]*Region),
		apikeys:   make(map[string]*APIKey),
	}
	b, err := p.Load()
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
		for _, rg := range snap.Regions {
			s.regions[rg.ID] = rg
		}
		for _, ak := range snap.APIKeys {
			s.apikeys[ak.ID] = ak
		}
		s.settings = snap.Settings
	}
	return s, nil
}

// ---- API keys ----

func (s *memStore) PutAPIKey(ak *APIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *ak
	s.apikeys[ak.ID] = &cp
	return s.flushLocked()
}

func (s *memStore) ListAPIKeys() []*APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*APIKey, 0, len(s.apikeys))
	for _, ak := range s.apikeys {
		cp := *ak
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *memStore) DeleteAPIKey(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.apikeys[id]; !ok {
		return ErrNotFound
	}
	delete(s.apikeys, id)
	return s.flushLocked()
}

// ---- Regions ----

func (s *memStore) PutRegion(rg *Region) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *rg
	s.regions[rg.ID] = &cp
	return s.flushLocked()
}

func (s *memStore) GetRegion(id string) (*Region, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rg, ok := s.regions[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *rg
	return &cp, nil
}

func (s *memStore) ListRegions() []*Region {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Region, 0, len(s.regions))
	for _, rg := range s.regions {
		cp := *rg
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *memStore) DeleteRegion(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.regions[id]; !ok {
		return ErrNotFound
	}
	delete(s.regions, id)
	return s.flushLocked()
}

// GetSettings returns a copy of the current platform settings.
func (s *memStore) GetSettings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// SetSettings replaces the platform settings and flushes to disk.
func (s *memStore) SetSettings(st Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = st
	return s.flushLocked()
}

// ---- Projects ----

func (s *memStore) PutProject(p *Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	s.projects[p.ID] = &cp
	return s.flushLocked()
}

func (s *memStore) GetProject(id string) (*Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (s *memStore) ListProjects() []*Project {
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
func (s *memStore) DeleteProject(id string) error {
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

func (s *memStore) PutWorkload(w *Workload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *w
	s.workloads[w.ID] = &cp
	return s.flushLocked()
}

func (s *memStore) GetWorkload(id string) (*Workload, error) {
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
func (s *memStore) ListWorkloads(projectID string) []*Workload {
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

func (s *memStore) SetWorkloadState(id string, state runtime.State) error {
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
func (s *memStore) DeleteWorkload(id string) error {
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

func (s *memStore) PutRoute(r *Route) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *r
	cp.Host = strings.ToLower(cp.Host)
	s.routes[cp.Host] = &cp
	return s.flushLocked()
}

// GetRouteByHost resolves a hostname (case-insensitive) to its route.
func (s *memStore) GetRouteByHost(host string) (*Route, error) {
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
func (s *memStore) GetRouteByKey(key string) (*Route, error) {
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

// DeleteRoute removes a single route by host.
func (s *memStore) DeleteRoute(host string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	host = strings.ToLower(host)
	if _, ok := s.routes[host]; !ok {
		return ErrNotFound
	}
	delete(s.routes, host)
	return s.flushLocked()
}

func (s *memStore) ListRoutesForWorkload(workloadID string) []*Route {
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

func (s *memStore) deleteRoutesForWorkloadLocked(workloadID string) {
	for host, r := range s.routes {
		if r.WorkloadID == workloadID {
			delete(s.routes, host)
		}
	}
}

// flushLocked writes the whole store atomically. Caller must hold s.mu.
// No-op in in-memory mode (empty path).
func (s *memStore) flushLocked() error {
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
	for _, rg := range s.regions {
		snap.Regions = append(snap.Regions, rg)
	}
	for _, ak := range s.apikeys {
		snap.APIKeys = append(snap.APIKeys, ak)
	}
	snap.Settings = s.settings
	sort.Slice(snap.Projects, func(i, j int) bool { return snap.Projects[i].CreatedAt.Before(snap.Projects[j].CreatedAt) })
	sort.Slice(snap.Workloads, func(i, j int) bool { return snap.Workloads[i].CreatedAt.Before(snap.Workloads[j].CreatedAt) })
	sort.Slice(snap.Routes, func(i, j int) bool { return snap.Routes[i].Host < snap.Routes[j].Host })

	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return s.persister.Save(b)
}
