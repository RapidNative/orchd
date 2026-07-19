// Package manager is the control-plane brain: it provisions projects and their
// workloads, mints credentials, assigns routes, and owns the wake/scale-to-zero
// lifecycle. All instance lifecycle is keyed by workload ID; the gateway resolves
// a hostname to a workload via the route table, then calls EnsureRunning.
package manager

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

type Manager struct {
	cfg   config.Config
	store *store.Store
	rt    runtime.Runtime

	mu       sync.Mutex
	live     map[string]*liveInstance // workloadID -> running instance
	refLocks map[string]*sync.Mutex   // per-workload wake serialization
}

type liveInstance struct {
	addr     string
	lastSeen time.Time
}

// WorkloadSpec describes a workload to create within a project.
type WorkloadSpec struct {
	Preset   string // optional: resolves Type/Image/Port/Name from the catalog
	Type     runtime.WorkloadType
	Name     string  // role within the project ("" = primary, else "api"/"web"/...)
	Image    string  // optional image override
	Port     int     // optional container port override
	MemoryMB int     // optional memory cap override (0 = type default)
	CPUs     float64 // optional CPU cap override (0 = type default)
}

// preset is a named workload template so the API/UI can ask for "expo"/"vite"/
// "api"/"tinbase" without spelling out image + port each time.
type preset struct {
	Type  runtime.WorkloadType
	Name  string
	Image string // empty = driver default (tinbase image)
	Port  int    // 0 = driver default (54321)
}

// Catalog is the built-in workload catalog. tinbase uses the driver default
// image/port; the RapidNative runners are their own images listening on 8080.
var Catalog = map[string]preset{
	"tinbase": {Type: runtime.WorkloadTinbaseProject, Name: "", Image: "", Port: 0},
	"expo":    {Type: runtime.WorkloadRapidNativeDev, Name: "app", Image: "rn-expo:dev", Port: 8080},
	"vite":    {Type: runtime.WorkloadRapidNativeDev, Name: "web", Image: "rn-vite:dev", Port: 8080},
	"api":     {Type: runtime.WorkloadRapidNativeDev, Name: "api", Image: "rn-api:dev", Port: 8080},
}

func New(cfg config.Config, st *store.Store, rt runtime.Runtime) *Manager {
	return &Manager{
		cfg:      cfg,
		store:    st,
		rt:       rt,
		live:     make(map[string]*liveInstance),
		refLocks: make(map[string]*sync.Mutex),
	}
}

// CreateProject creates a project grouping and provisions its workloads. With no
// specs it defaults to a single primary tinbase workload, preserving the simple
// "one project, one backend" case.
func (m *Manager) CreateProject(ctx context.Context, name string, specs []WorkloadSpec) (*store.Project, []*store.Workload, error) {
	ref, err := newRef()
	if err != nil {
		return nil, nil, err
	}
	proj := &store.Project{ID: ref, Name: name, Region: m.cfg.Region, CreatedAt: time.Now()}
	if err := m.store.PutProject(proj); err != nil {
		return nil, nil, err
	}

	if len(specs) == 0 {
		specs = []WorkloadSpec{{Type: runtime.WorkloadTinbaseProject}}
	}
	var created []*store.Workload
	for _, ws := range specs {
		w, err := m.AddWorkload(ctx, proj.ID, ws)
		if err != nil {
			return proj, created, fmt.Errorf("workload %q: %w", ws.Name, err)
		}
		created = append(created, w)
	}
	return proj, created, nil
}

// AddWorkload provisions one workload into an existing project: mint credentials
// (tinbase types), allocate a data dir, assign a route, and boot it.
func (m *Manager) AddWorkload(ctx context.Context, projectID string, ws WorkloadSpec) (*store.Workload, error) {
	proj, err := m.store.GetProject(projectID)
	if err != nil {
		return nil, err
	}
	// Resolve a preset (explicit fields still win if set).
	if ws.Preset != "" {
		p, ok := Catalog[ws.Preset]
		if !ok {
			return nil, fmt.Errorf("unknown preset %q", ws.Preset)
		}
		if ws.Type == "" {
			ws.Type = p.Type
		}
		if ws.Name == "" {
			ws.Name = p.Name
		}
		if ws.Image == "" {
			ws.Image = p.Image
		}
		if ws.Port == 0 {
			ws.Port = p.Port
		}
	}
	if ws.Type == "" {
		ws.Type = runtime.WorkloadTinbaseProject
	}

	wid, err := newRef()
	if err != nil {
		return nil, err
	}
	secret, err := newSecret()
	if err != nil {
		return nil, err
	}

	var anon, svc string
	if ws.Type == runtime.WorkloadTinbaseProject {
		anon, svc = m.mintKeys(secret) // best-effort
	}

	// Resource caps: explicit override wins, else default by workload type.
	mem, cpus := ws.MemoryMB, ws.CPUs
	if mem == 0 || cpus == 0 {
		dMem, dCPUs := m.defaultLimits(ws.Type)
		if mem == 0 {
			mem = dMem
		}
		if cpus == 0 {
			cpus = dCPUs
		}
	}

	w := &store.Workload{
		ID:        wid,
		ProjectID: proj.ID,
		Type:      ws.Type,
		Name:      ws.Name,
		Image:     ws.Image,
		Port:      ws.Port,
		MemoryMB:  mem,
		CPUs:      cpus,
		State:     runtime.StateProvisioning,
		DataDir:   filepath.Join(m.cfg.DataRoot, "projects", proj.ID, wid),
		JWTSecret: secret,
		AnonKey:   anon,
		SvcKey:    svc,
		CreatedAt: time.Now(),
	}
	if err := m.store.PutWorkload(w); err != nil {
		return nil, err
	}

	// Assign the default route. Key is <ref> for the primary workload,
	// <ref>-<name> for named ones; it drives both the subdomain (<key>.<base>)
	// and the subroute (/w/<key>). Additional routes can be attached via AddRoute.
	key := m.keyFor(proj.ID, ws.Name)
	host := key + "." + m.cfg.BaseDomain
	if err := m.store.PutRoute(&store.Route{Host: host, Key: key, WorkloadID: wid, CreatedAt: time.Now()}); err != nil {
		return nil, err
	}

	inst, err := m.rt.Create(ctx, m.specFor(w))
	if err != nil {
		_ = m.store.SetWorkloadState(wid, runtime.StateFailed)
		return nil, fmt.Errorf("provision: %w", err)
	}
	_ = m.store.SetWorkloadState(wid, runtime.StateRunning)
	m.markLive(wid, inst.Addr)

	return m.store.GetWorkload(wid)
}

// AddRoute attaches an additional hostname to a workload (e.g. a custom domain).
func (m *Manager) AddRoute(host, workloadID string) error {
	if _, err := m.store.GetWorkload(workloadID); err != nil {
		return err
	}
	return m.store.PutRoute(&store.Route{Host: host, Key: host, WorkloadID: workloadID, CreatedAt: time.Now()})
}

// ResolveHost maps a request hostname to its workload via the route table.
func (m *Manager) ResolveHost(host string) (*store.Workload, error) {
	r, err := m.store.GetRouteByHost(host)
	if err != nil {
		return nil, err
	}
	return m.store.GetWorkload(r.WorkloadID)
}

// ResolveKey maps a subroute key to its workload via the route table.
func (m *Manager) ResolveKey(key string) (*store.Workload, error) {
	r, err := m.store.GetRouteByKey(key)
	if err != nil {
		return nil, err
	}
	return m.store.GetWorkload(r.WorkloadID)
}

// EnsureRunning returns the address to proxy to for a workload, waking a
// suspended/stopped one on demand. The gateway hot path.
func (m *Manager) EnsureRunning(ctx context.Context, workloadID string) (string, error) {
	w, err := m.store.GetWorkload(workloadID)
	if err != nil {
		return "", err
	}

	rl := m.refLock(workloadID)
	rl.Lock()
	defer rl.Unlock()

	m.mu.Lock()
	li, ok := m.live[workloadID]
	m.mu.Unlock()
	if ok {
		if st, _ := m.rt.Status(ctx, workloadID); st == runtime.StateRunning {
			m.touch(workloadID)
			return li.addr, nil
		}
	}

	inst, err := m.rt.Start(ctx, m.specFor(w))
	if err != nil {
		return "", fmt.Errorf("wake %s: %w", workloadID, err)
	}
	_ = m.store.SetWorkloadState(workloadID, runtime.StateRunning)
	m.markLive(workloadID, inst.Addr)
	return inst.Addr, nil
}

func (m *Manager) Touch(workloadID string) { m.touch(workloadID) }

// DeleteProject stops every workload in a project, removes all its records, and
// reclaims the project's on-disk data.
func (m *Manager) DeleteProject(ctx context.Context, projectID string) error {
	if _, err := m.store.GetProject(projectID); err != nil {
		return err
	}
	for _, w := range m.store.ListWorkloads(projectID) {
		_ = m.rt.Stop(ctx, w.ID)
		m.forget(w.ID)
	}
	if err := m.store.DeleteProject(projectID); err != nil {
		return err
	}
	m.reclaimPath(filepath.Join(m.cfg.DataRoot, "projects", projectID))
	return nil
}

// DeleteWorkload stops and removes a single workload (routes + on-disk data).
func (m *Manager) DeleteWorkload(ctx context.Context, workloadID string) error {
	w, err := m.store.GetWorkload(workloadID)
	if err != nil {
		return err
	}
	_ = m.rt.Stop(ctx, workloadID)
	m.forget(workloadID)
	if err := m.store.DeleteWorkload(workloadID); err != nil {
		return err
	}
	m.reclaimPath(w.DataDir)
	return nil
}

// ReapIdle suspends workloads idle longer than the configured timeout.
func (m *Manager) ReapIdle(ctx context.Context) {
	now := time.Now()
	m.mu.Lock()
	var stale []string
	for id, li := range m.live {
		if now.Sub(li.lastSeen) > m.cfg.IdleTimeout {
			stale = append(stale, id)
		}
	}
	m.mu.Unlock()

	for _, id := range stale {
		rl := m.refLock(id)
		rl.Lock()
		if err := m.rt.Suspend(ctx, id); err == nil {
			_ = m.store.SetWorkloadState(id, runtime.StateSuspended)
			m.forget(id)
		}
		rl.Unlock()
	}
}

func (m *Manager) RunReaper(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.ReapIdle(ctx)
		}
	}
}

func (m *Manager) Store() *store.Store { return m.store }

// keyFor builds the route key for a workload: <ref> for the primary, else
// <ref>-<name>. Used for both subdomain and subroute addressing.
func (m *Manager) keyFor(ref, name string) string {
	if name == "" {
		return ref
	}
	return ref + "-" + name
}

func (m *Manager) specFor(w *store.Workload) runtime.Spec {
	return runtime.Spec{
		Type:    w.Type,
		Ref:     w.ID, // runtime key = workload id
		DataDir: w.DataDir,
		Image:   w.Image,
		Port:    w.Port,
		Env: map[string]string{
			"TINBASE_JWT_SECRET": w.JWTSecret,
		},
		Limits: runtime.Limits{
			MemoryMB:  w.MemoryMB,
			CPUs:      w.CPUs,
			PidsLimit: m.cfg.PidsLimit,
		},
	}
}

// defaultLimits returns the config default memory/CPU caps for a workload type.
func (m *Manager) defaultLimits(t runtime.WorkloadType) (memMB int, cpus float64) {
	if t == runtime.WorkloadRapidNativeDev {
		return m.cfg.DevMemMB, m.cfg.DevCPUs
	}
	return m.cfg.TinbaseMemMB, m.cfg.TinbaseCPUs
}

// reclaimPath removes an on-disk data path (a workload volume or a project dir)
// after its containers are gone. It refuses to delete anything outside the
// configured data root, as a guard against a bad path wiping the wrong thing.
func (m *Manager) reclaimPath(dir string) {
	if dir == "" {
		return
	}
	root := filepath.Clean(m.cfg.DataRoot)
	d := filepath.Clean(dir)
	if root == "" || d == root || !strings.HasPrefix(d, root+string(os.PathSeparator)) {
		log.Printf("reclaim: refusing to remove %q (outside data root %q)", d, root)
		return
	}
	if err := os.RemoveAll(d); err != nil {
		log.Printf("reclaim: %s: %v", d, err)
	}
}

func (m *Manager) markLive(id, addr string) {
	m.mu.Lock()
	m.live[id] = &liveInstance{addr: addr, lastSeen: time.Now()}
	m.mu.Unlock()
}

func (m *Manager) forget(id string) {
	m.mu.Lock()
	delete(m.live, id)
	m.mu.Unlock()
}

func (m *Manager) touch(id string) {
	m.mu.Lock()
	if li, ok := m.live[id]; ok {
		li.lastSeen = time.Now()
	}
	m.mu.Unlock()
}

func (m *Manager) refLock(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.refLocks[id]
	if !ok {
		l = &sync.Mutex{}
		m.refLocks[id] = l
	}
	return l
}

// mintKeys derives anon/service_role keys for a JWT secret by asking tinbase.
// Best-effort: empty if unavailable.
func (m *Manager) mintKeys(secret string) (anon, svc string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch {
	case m.cfg.Driver == "docker":
		cmd = exec.CommandContext(ctx, "docker", "run", "--rm", m.cfg.Image, "tinbase", "keys", "--jwt-secret", secret)
	case filepath.Ext(m.cfg.TinbaseBin) == ".js":
		cmd = exec.CommandContext(ctx, "node", m.cfg.TinbaseBin, "keys", "--jwt-secret", secret)
	default:
		cmd = exec.CommandContext(ctx, m.cfg.TinbaseBin, "keys", "--jwt-secret", secret)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", ""
	}
	tokens := jwtRe.FindAllString(out.String(), -1)
	if len(tokens) >= 2 {
		return tokens[0], tokens[1]
	}
	if len(tokens) == 1 {
		return tokens[0], ""
	}
	return "", ""
}

var jwtRe = regexp.MustCompile(`eyJ[A-Za-z0-9._-]{10,}`)

func newRef() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	if b[0] >= '0' && b[0] <= '9' {
		b[0] = 'a' // valid DNS label: start with a letter
	}
	return string(b), nil
}

func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
