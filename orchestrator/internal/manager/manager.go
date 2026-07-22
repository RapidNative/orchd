// Package manager is the control-plane brain: it provisions projects and their
// workloads, mints credentials, assigns routes, and owns the wake/scale-to-zero
// lifecycle. All instance lifecycle is keyed by workload ID; the gateway resolves
// a hostname to a workload via the route table, then calls EnsureRunning.
package manager

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/backup"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/events"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/metrics"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/template"
)

// ErrBackupsDisabled is returned when a backup operation is requested but no
// backup store is configured.
var ErrBackupsDisabled = errors.New("backups not configured")

// deltaBackupExclude are derived/regenerable directories left out of workload
// backups, so a backup captures only the user's files (and tinbase data) — the
// delta. Deps come from npm install (local) or the image (prod), never a backup.
var deltaBackupExclude = []string{
	"node_modules", ".git", "dist", "build", ".next", ".expo", ".cache", ".turbo",
}
var ErrImagesUnsupported = errors.New("image management not supported by this runtime")

type Manager struct {
	cfg     config.Config
	store   store.Store
	rt      runtime.Runtime
	backups backup.Store // nil when backups are disabled

	mem     *events.MemorySink // activity feed (always on)
	sink    events.Sink        // fan-out sink (mem + optional webhook)
	metrics metrics.Sink       // metrics publish target

	mu       sync.Mutex
	live     map[string]*liveInstance // workloadID -> running instance
	refLocks map[string]*sync.Mutex   // per-workload wake serialization

	portMu sync.Mutex // serializes host-port allocation (port-addressing mode)
}

type liveInstance struct {
	addr     string
	lastSeen time.Time
}

// WorkloadSpec describes a workload to create within a project.
type WorkloadSpec struct {
	Preset      string // optional: resolves Type/Image/Port/Name from the catalog
	Type        runtime.WorkloadType
	Name        string            // role within the project ("" = primary, else "api"/"web"/...)
	Image       string            // optional image override
	Port        int               // optional container port override
	MemoryMB    int               // optional memory cap override (0 = type default)
	CPUs        float64           // optional CPU cap override (0 = type default)
	Template    string            // template name (local template mode)
	Workspace   string            // orchd.json workspace name within the template
	Env         map[string]string // user-injected environment variables
	SeedDelta   map[string]string // files to overlay on the base at seed (path -> content)
	SeedDeleted []string          // base paths to remove at seed (tombstones)
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

func New(cfg config.Config, st store.Store, rt runtime.Runtime) *Manager {
	m := &Manager{
		cfg:      cfg,
		store:    st,
		rt:       rt,
		mem:      events.NewMemorySink(500),
		live:     make(map[string]*liveInstance),
		refLocks: make(map[string]*sync.Mutex),
	}
	m.applyBackupSettings() // build the backup store from persisted settings (or default)
	m.applyWebhookSettings()
	m.applyMetricsSettings()
	m.seedRegions()
	m.seedTemplates()
	return m
}

// ---- metrics ----

func (m *Manager) buildMetricsSink(t store.MetricsTarget) metrics.Sink {
	switch t.Type {
	case "log":
		return metrics.LogSink{}
	case "http":
		if t.URL != "" {
			return metrics.NewHTTPSink(t.URL)
		}
	}
	return metrics.NopSink{}
}

func (m *Manager) applyMetricsSettings() {
	s := m.buildMetricsSink(m.store.GetSettings().Metrics)
	m.mu.Lock()
	m.metrics = s
	m.mu.Unlock()
}

func (m *Manager) metricsSink() metrics.Sink {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.metrics
}

func (m *Manager) GetMetricsTarget() store.MetricsTarget { return m.store.GetSettings().Metrics }

func (m *Manager) SetMetricsTarget(t store.MetricsTarget) error {
	s := m.store.GetSettings()
	s.Metrics = t
	if err := m.store.SetSettings(s); err != nil {
		return err
	}
	m.applyMetricsSettings()
	return nil
}

// Snapshot computes a fleet snapshot from the store (cheap).
func (m *Manager) Snapshot() metrics.Snapshot {
	wls := m.store.ListWorkloads("")
	running, suspended, mem := 0, 0, 0
	for _, w := range wls {
		switch w.State {
		case runtime.StateRunning:
			running++
			mem += w.MemoryMB
		case runtime.StateSuspended:
			suspended++
		}
	}
	return metrics.Snapshot{
		Time: time.Now().UTC(), Projects: len(m.store.ListProjects()),
		Workloads: len(wls), Running: running, Suspended: suspended, MemMBAllocated: mem,
	}
}

// RunMetricsPublisher publishes a snapshot on the configured interval.
func (m *Manager) RunMetricsPublisher(ctx context.Context) {
	interval := m.cfg.MetricsInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.metricsSink().Publish(m.Snapshot())
		}
	}
}

// ---- regions ----

func (m *Manager) seedRegions() {
	if len(m.store.ListRegions()) > 0 {
		return
	}
	id := slug(m.cfg.Region)
	if id == "" {
		id = "local"
	}
	_ = m.store.PutRegion(&store.Region{ID: id, Name: m.cfg.Region, IsDefault: true, CreatedAt: time.Now()})
}

func (m *Manager) ListRegions() []*store.Region { return m.store.ListRegions() }

// ---- API keys (control-plane credentials with roles) ----

func hashKey(k string) string {
	s := sha256.Sum256([]byte(k))
	return hex.EncodeToString(s[:])
}

// CreateAPIKey mints a key with a role and returns the plaintext ONCE. Only the
// hash is persisted.
func (m *Manager) CreateAPIKey(name, role string) (*store.APIKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", fmt.Errorf("key name required")
	}
	if role != "admin" && role != "readonly" {
		role = "readonly"
	}
	raw, err := newSecret()
	if err != nil {
		return nil, "", err
	}
	key := "tbk_" + raw
	id, err := newRef()
	if err != nil {
		return nil, "", err
	}
	ak := &store.APIKey{ID: id, Name: name, Role: role, Hash: hashKey(key), CreatedAt: time.Now()}
	if err := m.store.PutAPIKey(ak); err != nil {
		return nil, "", err
	}
	m.emit("apikey.created", "", "", name+" ("+role+")")
	out := *ak
	out.Hash = ""
	return &out, key, nil
}

// ListAPIKeys lists keys with hashes stripped.
func (m *Manager) ListAPIKeys() []*store.APIKey {
	keys := m.store.ListAPIKeys()
	for _, k := range keys {
		k.Hash = ""
	}
	return keys
}

func (m *Manager) DeleteAPIKey(id string) error {
	if err := m.store.DeleteAPIKey(id); err != nil {
		return err
	}
	m.emit("apikey.deleted", "", "", id)
	return nil
}

// ValidateStoredKey returns the role for a presented key if it matches a stored
// one (constant-time), else ok=false.
func (m *Manager) ValidateStoredKey(presented string) (string, bool) {
	if presented == "" {
		return "", false
	}
	h := hashKey(presented)
	for _, ak := range m.store.ListAPIKeys() {
		if subtle.ConstantTimeCompare([]byte(ak.Hash), []byte(h)) == 1 {
			return ak.Role, true
		}
	}
	return "", false
}

func (m *Manager) defaultRegionID() string {
	regions := m.store.ListRegions()
	for _, rg := range regions {
		if rg.IsDefault {
			return rg.ID
		}
	}
	if len(regions) > 0 {
		return regions[0].ID
	}
	return slug(m.cfg.Region)
}

func (m *Manager) CreateRegion(name, dockerHost string) (*store.Region, error) {
	name = strings.TrimSpace(name)
	id := slug(name)
	if id == "" {
		return nil, fmt.Errorf("valid region name required")
	}
	if _, err := m.store.GetRegion(id); err == nil {
		return nil, fmt.Errorf("region %q already exists", id)
	}
	rg := &store.Region{
		ID: id, Name: name, DockerHost: dockerHost,
		IsDefault: len(m.store.ListRegions()) == 0, CreatedAt: time.Now(),
	}
	if err := m.store.PutRegion(rg); err != nil {
		return nil, err
	}
	m.emit("region.created", "", "", id)
	return rg, nil
}

func (m *Manager) DeleteRegion(id string) error {
	rg, err := m.store.GetRegion(id)
	if err != nil {
		return err
	}
	if rg.IsDefault {
		return fmt.Errorf("cannot delete the default region; set another default first")
	}
	if err := m.store.DeleteRegion(id); err != nil {
		return err
	}
	m.emit("region.deleted", "", "", id)
	return nil
}

func (m *Manager) SetDefaultRegion(id string) error {
	if _, err := m.store.GetRegion(id); err != nil {
		return err
	}
	for _, rg := range m.store.ListRegions() {
		want := rg.ID == id
		if rg.IsDefault != want {
			rg.IsDefault = want
			_ = m.store.PutRegion(rg)
		}
	}
	return nil
}

// slug lowercases a name and turns runs of non-alphanumerics into single dashes.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case b.Len() > 0 && !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// ---- events / activity ----

func (m *Manager) emit(evType, projectID, workloadID, msg string) {
	m.mu.Lock()
	sink := m.sink
	m.mu.Unlock()
	if sink == nil {
		return
	}
	sink.Emit(events.Event{
		ID: events.NewID(), Time: time.Now().UTC(), Type: evType,
		ProjectID: projectID, WorkloadID: workloadID, Message: msg,
	})
}

func (m *Manager) applyWebhookSettings() {
	url := m.store.GetSettings().Webhook.URL
	m.mu.Lock()
	if url != "" {
		m.sink = events.NewMultiSink(m.mem, events.NewWebhookSink(url))
	} else {
		m.sink = m.mem
	}
	m.mu.Unlock()
}

// Events returns up to n recent activity events, newest first.
func (m *Manager) Events(n int) []events.Event { return m.mem.Recent(n) }

// GetWebhook / SetWebhook manage the event webhook URL.
// GetInstanceName returns this deployment's operator-chosen name (may be empty).
func (m *Manager) GetInstanceName() string { return m.store.GetSettings().InstanceName }

// SetInstanceName sets this deployment's display name (e.g. "tinbase cloud").
func (m *Manager) SetInstanceName(name string) error {
	s := m.store.GetSettings()
	s.InstanceName = strings.TrimSpace(name)
	return m.store.SetSettings(s)
}

func (m *Manager) GetWebhook() string { return m.store.GetSettings().Webhook.URL }

func (m *Manager) SetWebhook(url string) error {
	s := m.store.GetSettings()
	s.Webhook.URL = url
	if err := m.store.SetSettings(s); err != nil {
		return err
	}
	m.applyWebhookSettings()
	return nil
}

// backupStore returns the current backup store (nil = disabled), guarded so it
// can be swapped at runtime when the target is reconfigured.
func (m *Manager) backupStore() backup.Store {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.backups
}

func (m *Manager) buildBackupStore(t store.BackupTarget) (backup.Store, error) {
	switch t.Type {
	case "s3":
		return backup.NewS3Store(t.Endpoint, t.Bucket, t.Region, t.Prefix, t.AccessKey, t.SecretKey)
	default: // "local" or unset
		if m.cfg.BackupDir == "" {
			return nil, nil // backups disabled
		}
		return backup.NewLocalStore(m.cfg.BackupDir)
	}
}

func (m *Manager) applyBackupSettings() {
	t := m.store.GetSettings().Backup
	bs, err := m.buildBackupStore(t)
	if err != nil {
		log.Printf("backup store (%s) invalid: %v; falling back to local", t.Type, err)
		bs, _ = m.buildBackupStore(store.BackupTarget{Type: "local"})
	}
	m.mu.Lock()
	m.backups = bs
	m.mu.Unlock()
}

// GetBackupTarget returns the configured backup target.
func (m *Manager) GetBackupTarget() store.BackupTarget { return m.store.GetSettings().Backup }

// SetBackupTarget validates and persists a new backup target, then swaps the
// live backup store to it.
func (m *Manager) SetBackupTarget(t store.BackupTarget) error {
	bs, err := m.buildBackupStore(t) // validates the config by constructing it
	if err != nil {
		return err
	}
	s := m.store.GetSettings()
	s.Backup = t
	if err := m.store.SetSettings(s); err != nil {
		return err
	}
	m.mu.Lock()
	m.backups = bs
	m.mu.Unlock()
	return nil
}

// templatePath resolves a template name to its local folder: Settings first,
// then ORCHD_TEMPLATE_<NAME>. Empty if unknown.
func (m *Manager) templatePath(name string) string {
	if name == "" {
		return ""
	}
	if t := m.store.GetSettings().Templates; t != nil {
		if p := t[name]; p != "" {
			return p
		}
	}
	return os.Getenv("ORCHD_TEMPLATE_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")))
}

// seedTemplates auto-registers every subdir of cfg.TemplatesDir that has an
// orchd.json, so bundled example templates are available on a fresh clone
// without manual setup. It never overwrites a template a user already set.
func (m *Manager) seedTemplates() {
	if m.cfg.TemplatesDir == "" {
		return
	}
	entries, err := os.ReadDir(m.cfg.TemplatesDir)
	if err != nil {
		return
	}
	existing := m.store.GetSettings().Templates
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, ok := existing[e.Name()]; ok {
			continue // user-registered wins
		}
		dir := filepath.Join(m.cfg.TemplatesDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, template.FileName)); err != nil {
			continue // not a template
		}
		if err := m.SetTemplate(e.Name(), dir); err == nil {
			log.Printf("template seeded: %s -> %s", e.Name(), dir)
		}
	}
}

// GetTemplates returns the configured template name->path map.
func (m *Manager) GetTemplates() map[string]string { return m.store.GetSettings().Templates }

// ---- template base (served + browsable) ----

func (m *Manager) TemplateManifest(name string) (*template.Manifest, error) {
	p := m.templatePath(name)
	if p == "" {
		return nil, fmt.Errorf("template %q not configured", name)
	}
	return template.Load(p)
}

func (m *Manager) templateExcludes(name string) (string, []string, error) {
	p := m.templatePath(name)
	if p == "" {
		return "", nil, fmt.Errorf("template %q not configured", name)
	}
	man, err := template.Load(p)
	if err != nil {
		return p, nil, nil // still browsable even without a valid manifest
	}
	return p, man.BackupExclude, nil
}

func (m *Manager) TemplateFiles(name string) ([]string, error) {
	p, ex, err := m.templateExcludes(name)
	if err != nil {
		return nil, err
	}
	return template.ListFiles(p, ex)
}

func (m *Manager) TemplateFile(name, rel string) ([]byte, error) {
	p, _, err := m.templateExcludes(name)
	if err != nil {
		return nil, err
	}
	return template.ReadFile(p, rel)
}

func (m *Manager) TemplateBundle(name string, w io.Writer) error {
	p, ex, err := m.templateExcludes(name)
	if err != nil {
		return err
	}
	return template.Bundle(p, ex, w)
}

// ---- live workload filesystem (the materialized base + delta) ----

func (m *Manager) WorkloadFiles(id string) ([]string, error) {
	w, err := m.store.GetWorkload(id)
	if err != nil {
		return nil, err
	}
	return template.ListFiles(w.DataDir, deltaBackupExclude)
}

func (m *Manager) WorkloadFile(id, rel string) ([]byte, error) {
	w, err := m.store.GetWorkload(id)
	if err != nil {
		return nil, err
	}
	return template.ReadFile(w.DataDir, rel)
}

// WriteWorkloadFile writes into the running workload's tree. The dev server's
// file watcher picks it up (HMR). No reboot.
func (m *Manager) WriteWorkloadFile(id, rel string, content []byte) error {
	w, err := m.store.GetWorkload(id)
	if err != nil {
		return err
	}
	return template.WriteFile(w.DataDir, rel, content)
}

func (m *Manager) DeleteWorkloadFile(id, rel string) error {
	w, err := m.store.GetWorkload(id)
	if err != nil {
		return err
	}
	return template.DeleteFile(w.DataDir, rel)
}

// rebootWorkload recreates a workload's instance (stop -> create), picking up
// the current spec — new env, etc. Serialized per workload via refLock.
func (m *Manager) rebootWorkload(ctx context.Context, w *store.Workload) error {
	rl := m.refLock(w.ID)
	rl.Lock()
	defer rl.Unlock()
	_ = m.rt.Stop(ctx, w.ID)
	m.forget(w.ID)
	inst, err := m.rt.Create(ctx, m.specFor(w))
	if err != nil {
		_ = m.store.SetWorkloadState(w.ID, runtime.StateFailed)
		return fmt.Errorf("reboot: %w", err)
	}
	_ = m.store.SetWorkloadState(w.ID, runtime.StateRunning)
	m.markLive(w.ID, inst.Addr)
	return nil
}

// RestartWorkload recreates a workload (also the way to refresh its env).
func (m *Manager) RestartWorkload(ctx context.Context, workloadID string) error {
	w, err := m.store.GetWorkload(workloadID)
	if err != nil {
		return err
	}
	if err := m.rebootWorkload(ctx, w); err != nil {
		return err
	}
	m.emit("workload.restarted", w.ProjectID, workloadID, "")
	return nil
}

// StopWorkload suspends a workload (scale to zero) — the "pause".
func (m *Manager) StopWorkload(ctx context.Context, workloadID string) error {
	w, err := m.store.GetWorkload(workloadID)
	if err != nil {
		return err
	}
	rl := m.refLock(workloadID)
	rl.Lock()
	defer rl.Unlock()
	if err := m.rt.Suspend(ctx, workloadID); err != nil {
		return err
	}
	_ = m.store.SetWorkloadState(workloadID, runtime.StateSuspended)
	m.forget(workloadID)
	m.emit("workload.stopped", w.ProjectID, workloadID, "")
	return nil
}

// StartWorkload boots a suspended/stopped workload — the "play".
func (m *Manager) StartWorkload(ctx context.Context, workloadID string) error {
	w, err := m.store.GetWorkload(workloadID)
	if err != nil {
		return err
	}
	rl := m.refLock(workloadID)
	rl.Lock()
	defer rl.Unlock()
	if st, _ := m.rt.Status(ctx, workloadID); st == runtime.StateRunning {
		return nil
	}
	inst, err := m.rt.Start(ctx, m.specFor(w))
	if err != nil {
		_ = m.store.SetWorkloadState(workloadID, runtime.StateFailed)
		return fmt.Errorf("start: %w", err)
	}
	_ = m.store.SetWorkloadState(workloadID, runtime.StateRunning)
	m.markLive(workloadID, inst.Addr)
	m.emit("workload.started", w.ProjectID, workloadID, "")
	return nil
}

// forEachWorkload runs fn over every workload of a project, returning the first error.
func (m *Manager) forEachWorkload(projectID string, fn func(id string) error) error {
	if _, err := m.store.GetProject(projectID); err != nil {
		return err
	}
	var firstErr error
	for _, w := range m.store.ListWorkloads(projectID) {
		if err := fn(w.ID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// StartProject / StopProject / RestartProject apply the action to every workload.
func (m *Manager) StartProject(ctx context.Context, id string) error {
	return m.forEachWorkload(id, func(w string) error { return m.StartWorkload(ctx, w) })
}
func (m *Manager) StopProject(ctx context.Context, id string) error {
	return m.forEachWorkload(id, func(w string) error { return m.StopWorkload(ctx, w) })
}
func (m *Manager) RestartProject(ctx context.Context, id string) error {
	return m.forEachWorkload(id, func(w string) error { return m.RestartWorkload(ctx, w) })
}

// SetWorkloadEnv replaces a workload's injected env and reboots it to apply.
func (m *Manager) SetWorkloadEnv(ctx context.Context, workloadID string, env map[string]string) error {
	w, err := m.store.GetWorkload(workloadID)
	if err != nil {
		return err
	}
	w.Env = env
	if err := m.store.PutWorkload(w); err != nil {
		return err
	}
	if err := m.rebootWorkload(ctx, w); err != nil {
		return err
	}
	m.emit("workload.env_updated", w.ProjectID, workloadID, "")
	return nil
}

// SetProjectEnv replaces a project's env (injected into every workload) and
// restarts its workloads to apply.
func (m *Manager) SetProjectEnv(ctx context.Context, projectID string, env map[string]string) error {
	p, err := m.store.GetProject(projectID)
	if err != nil {
		return err
	}
	p.Env = env
	if err := m.store.PutProject(p); err != nil {
		return err
	}
	m.emit("project.env_updated", projectID, "", "")
	return m.RestartProject(ctx, projectID)
}

// GetProjectEnv returns a project's injected env.
func (m *Manager) GetProjectEnv(projectID string) (map[string]string, error) {
	p, err := m.store.GetProject(projectID)
	if err != nil {
		return nil, err
	}
	return p.Env, nil
}

// SetTemplate registers (or, with an empty path, removes) a template path.
func (m *Manager) SetTemplate(name, path string) error {
	s := m.store.GetSettings()
	if s.Templates == nil {
		s.Templates = map[string]string{}
	}
	if strings.TrimSpace(path) == "" {
		delete(s.Templates, name)
	} else {
		s.Templates[name] = path
	}
	return m.store.SetSettings(s)
}

// CreateFromTemplate provisions a project whose workloads come from a template's
// orchd.json: one workload per manifest entry, each running its workspace on its
// own port (local) or from its image (prod).
func (m *Manager) CreateFromTemplate(ctx context.Context, tmplName, projName, regionID string, delta map[string]string, deleted []string) (*store.Project, []*store.Workload, error) {
	path := m.templatePath(tmplName)
	if path == "" {
		return nil, nil, fmt.Errorf("template %q is not configured (add it in Settings)", tmplName)
	}
	man, err := template.Load(path)
	if err != nil {
		return nil, nil, fmt.Errorf("template %q: %w", tmplName, err)
	}
	specs := make([]WorkloadSpec, 0, len(man.Workloads))
	for _, w := range man.Workloads {
		s := WorkloadSpec{Template: tmplName, Workspace: w.Name, Name: w.Name, Image: w.Image, Env: w.Env, SeedDelta: delta, SeedDeleted: deleted}
		if w.Kind == "tinbase" {
			s.Type = runtime.WorkloadTinbaseProject
			s.Name = "" // primary
		} else {
			s.Type = runtime.WorkloadRapidNativeDev
		}
		specs = append(specs, s)
	}
	if projName == "" {
		projName = man.Name
	}
	return m.CreateProject(ctx, projName, regionID, specs)
}

// CreateProject creates a project grouping and provisions its workloads. With no
// specs it defaults to a single primary tinbase workload, preserving the simple
// "one project, one backend" case.
func (m *Manager) CreateProject(ctx context.Context, name, regionID string, specs []WorkloadSpec) (*store.Project, []*store.Workload, error) {
	if regionID == "" {
		regionID = m.defaultRegionID()
	} else if _, err := m.store.GetRegion(regionID); err != nil {
		return nil, nil, fmt.Errorf("region %q: %w", regionID, err)
	}
	ref, err := newRef()
	if err != nil {
		return nil, nil, err
	}
	proj := &store.Project{ID: ref, Name: name, Region: regionID, CreatedAt: time.Now()}
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
	m.emit("project.created", proj.ID, "", name)
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
		HostPort:  m.allocHostPort(),
		Template:  ws.Template,
		Workspace: ws.Workspace,
		Env:       ws.Env,
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

	// Materialize a template workload's working tree now (base copy + delta), and
	// mark it seeded so the driver runs it as-is. Fast (file copy); the slow part
	// (npm install) stays in the async boot below.
	if w.Template != "" {
		if base := m.templatePath(w.Template); base != "" {
			var excl []string
			if man, err := template.Load(base); err == nil {
				excl = man.BackupExclude
			}
			if err := template.Materialize(base, w.DataDir, excl, ws.SeedDelta, ws.SeedDeleted); err != nil {
				log.Printf("materialize %s: %v", wid, err)
			} else {
				_ = os.WriteFile(filepath.Join(w.DataDir, ".orchd-seeded"), []byte("ok\n"), 0o644)
			}
		}
	}

	m.emit("workload.created", proj.ID, wid, ws.Name)

	// Provision in the background so a slow first boot (npm install for a heavy
	// template workspace, an image pull, tinbase initdb) doesn't block the create
	// call. The workload starts "provisioning" and flips to running/failed; the
	// admin polls state. Synchronous errors above (bad spec, store) still return.
	go m.provision(wid)

	return m.store.GetWorkload(wid)
}

// provision boots a freshly-created workload in the background, updating its
// state to running or failed. Serialized per workload via refLock.
func (m *Manager) provision(workloadID string) {
	rl := m.refLock(workloadID)
	rl.Lock()
	defer rl.Unlock()
	w, err := m.store.GetWorkload(workloadID)
	if err != nil {
		return // deleted before provisioning ran
	}
	inst, err := m.rt.Create(context.Background(), m.specFor(w))
	if err != nil {
		log.Printf("provision %s: %v", workloadID, err)
		_ = m.store.SetWorkloadState(workloadID, runtime.StateFailed)
		m.emit("workload.failed", w.ProjectID, workloadID, err.Error())
		return
	}
	_ = m.store.SetWorkloadState(workloadID, runtime.StateRunning)
	m.markLive(workloadID, inst.Addr)
}

// AddRoute attaches an additional hostname to a workload (e.g. a custom domain).
func (m *Manager) AddRoute(host, workloadID string) error {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return fmt.Errorf("host required")
	}
	if _, err := m.store.GetWorkload(workloadID); err != nil {
		return err
	}
	if r, err := m.store.GetRouteByHost(host); err == nil && r.WorkloadID != workloadID {
		return fmt.Errorf("host %q already routes to another workload", host)
	}
	if err := m.store.PutRoute(&store.Route{Host: host, Key: host, WorkloadID: workloadID, CreatedAt: time.Now()}); err != nil {
		return err
	}
	w, _ := m.store.GetWorkload(workloadID)
	m.emit("route.added", projectOf(w), workloadID, host)
	return nil
}

// RemoveRoute detaches a hostname.
func (m *Manager) RemoveRoute(host string) error {
	host = strings.ToLower(strings.TrimSpace(host))
	r, err := m.store.GetRouteByHost(host)
	if err != nil {
		return err
	}
	if err := m.store.DeleteRoute(host); err != nil {
		return err
	}
	w, _ := m.store.GetWorkload(r.WorkloadID)
	m.emit("route.removed", projectOf(w), r.WorkloadID, host)
	return nil
}

func projectOf(w *store.Workload) string {
	if w == nil {
		return ""
	}
	return w.ProjectID
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

// Stats returns a live resource snapshot for a workload's instance.
func (m *Manager) Stats(ctx context.Context, workloadID string) (runtime.Stats, error) {
	if _, err := m.store.GetWorkload(workloadID); err != nil {
		return runtime.Stats{}, err
	}
	return m.rt.Stats(ctx, workloadID)
}

// Logs returns the last `tail` lines of a workload's output.
func (m *Manager) Logs(ctx context.Context, workloadID string, tail int) (string, error) {
	if _, err := m.store.GetWorkload(workloadID); err != nil {
		return "", err
	}
	return m.rt.Logs(ctx, workloadID, tail)
}

// ---- image management ----

// ImagesSupported reports whether the active runtime driver can manage container
// images (DockerDriver yes, LocalDriver no).
func (m *Manager) ImagesSupported() bool {
	_, ok := m.rt.(runtime.ImageManager)
	return ok
}

// dockerHostForRegion resolves a region id to its docker_host. An empty regionID
// means the default region; empty result means the local daemon.
func (m *Manager) dockerHostForRegion(regionID string) (string, error) {
	if regionID == "" {
		regionID = m.defaultRegionID()
	}
	if regionID == "" {
		return "", nil // no regions configured: local daemon
	}
	rg, err := m.store.GetRegion(regionID)
	if err != nil {
		return "", err
	}
	return rg.DockerHost, nil
}

// ListImages lists the container images available on a region's Docker host.
func (m *Manager) ListImages(ctx context.Context, regionID string) ([]runtime.ImageInfo, error) {
	im, ok := m.rt.(runtime.ImageManager)
	if !ok {
		return nil, ErrImagesUnsupported
	}
	host, err := m.dockerHostForRegion(regionID)
	if err != nil {
		return nil, err
	}
	return im.ListImages(ctx, host)
}

// PullImage pulls an image ref onto a region's Docker host, emits an event, and
// returns the CLI output.
func (m *Manager) PullImage(ctx context.Context, regionID, ref string) (string, error) {
	im, ok := m.rt.(runtime.ImageManager)
	if !ok {
		return "", ErrImagesUnsupported
	}
	host, err := m.dockerHostForRegion(regionID)
	if err != nil {
		return "", err
	}
	out, err := im.PullImage(ctx, host, ref)
	if err != nil {
		return out, err
	}
	m.emit("image.pulled", "", "", ref)
	return out, nil
}

// RemoveImage deletes an image from a region's Docker host and emits an event.
func (m *Manager) RemoveImage(ctx context.Context, regionID, ref string, force bool) error {
	im, ok := m.rt.(runtime.ImageManager)
	if !ok {
		return ErrImagesUnsupported
	}
	host, err := m.dockerHostForRegion(regionID)
	if err != nil {
		return err
	}
	if err := im.RemoveImage(ctx, host, ref, force); err != nil {
		return err
	}
	m.emit("image.removed", "", "", ref)
	return nil
}

// LastSeen reports when a running workload last served a request ("last box hit").
func (m *Manager) LastSeen(workloadID string) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if li, ok := m.live[workloadID]; ok {
		return li.lastSeen, true
	}
	return time.Time{}, false
}

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
	m.emit("project.deleted", projectID, "", "")
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
	m.emit("workload.deleted", w.ProjectID, workloadID, w.Name)
	return nil
}

// SetKeepWarm toggles always-on for a workload. Enabling boots it now and exempts
// it from scale-to-zero; disabling lets the reaper suspend it again when idle.
func (m *Manager) SetKeepWarm(ctx context.Context, workloadID string, enabled bool) error {
	w, err := m.store.GetWorkload(workloadID)
	if err != nil {
		return err
	}
	w.KeepWarm = enabled
	if err := m.store.PutWorkload(w); err != nil {
		return err
	}
	warm := "off"
	if enabled {
		warm = "on"
	}
	m.emit("workload.keepwarm", w.ProjectID, workloadID, warm)
	if enabled {
		if _, err := m.EnsureRunning(ctx, workloadID); err != nil {
			return err
		}
	}
	return nil
}

// WakeKeepWarm boots all always-on workloads (run at startup so they are up
// without waiting for a first request).
func (m *Manager) WakeKeepWarm(ctx context.Context) {
	for _, w := range m.store.ListWorkloads("") {
		if w.KeepWarm {
			if _, err := m.EnsureRunning(ctx, w.ID); err != nil {
				log.Printf("keepwarm wake %s: %v", w.ID, err)
			}
		}
	}
}

// ReapIdle suspends workloads idle longer than the configured timeout.
func (m *Manager) ReapIdle(ctx context.Context) {
	// IdleTimeout <= 0 disables scale-to-zero (everything stays warm). Used
	// locally, where workloads are addressed by their own port (bypassing the
	// gateway) so there is nothing to refresh idleness or wake a reaped one.
	if m.cfg.IdleTimeout <= 0 {
		return
	}
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
		if w, err := m.store.GetWorkload(id); err == nil && w.KeepWarm {
			continue // always-on: never scale to zero
		}
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

func (m *Manager) Store() store.Store { return m.store }

// ---- backups ----

// BackupsEnabled reports whether a backup store is configured.
func (m *Manager) BackupsEnabled() bool { return m.backupStore() != nil }

// BackupWorkload snapshots a workload's data volume. A running workload is
// briefly suspended (graceful, so Postgres flushes) for a byte-consistent
// archive, then brought back.
func (m *Manager) BackupWorkload(ctx context.Context, workloadID string) (backup.Backup, error) {
	bk := m.backupStore()
	if bk == nil {
		return backup.Backup{}, ErrBackupsDisabled
	}
	w, err := m.store.GetWorkload(workloadID)
	if err != nil {
		return backup.Backup{}, err
	}
	rl := m.refLock(workloadID)
	rl.Lock()
	defer rl.Unlock()

	wasRunning := false
	if st, _ := m.rt.Status(ctx, workloadID); st == runtime.StateRunning {
		wasRunning = true
		_ = m.rt.Suspend(ctx, workloadID)
		_ = m.store.SetWorkloadState(workloadID, runtime.StateSuspended)
		m.forget(workloadID)
	}

	b, err := bk.Create(workloadID, w.DataDir, deltaBackupExclude)

	if wasRunning {
		if inst, e := m.rt.Start(ctx, m.specFor(w)); e == nil {
			_ = m.store.SetWorkloadState(workloadID, runtime.StateRunning)
			m.markLive(workloadID, inst.Addr)
		}
	}
	if err != nil {
		return backup.Backup{}, err
	}
	_ = bk.Retain(workloadID, m.cfg.BackupRetain)
	m.emit("backup.created", w.ProjectID, workloadID, b.ID)
	return b, nil
}

// StateBackupKey is the reserved "workload id" the control-plane state snapshot
// is stored under, so it lives alongside tenant backups in the same store.
const StateBackupKey = "_control-plane"

// BackupState snapshots the control-plane state directory (the project/workload
// index — JSON file or SQLite db) into the backup store, so the source of truth
// survives box loss. For SQLite it checkpoints the WAL first so the copy is
// consistent. Restoring is a manual op (fetch + extract) since it re-seeds the
// whole control plane.
func (m *Manager) BackupState(ctx context.Context) (backup.Backup, error) {
	bk := m.backupStore()
	if bk == nil {
		return backup.Backup{}, ErrBackupsDisabled
	}
	if cp, ok := m.store.(interface{ Checkpoint() error }); ok {
		if err := cp.Checkpoint(); err != nil {
			log.Printf("state checkpoint: %v", err)
		}
	}
	stateDir := filepath.Join(m.cfg.DataRoot, "state")
	b, err := bk.Create(StateBackupKey, stateDir, nil)
	if err != nil {
		return backup.Backup{}, err
	}
	_ = bk.Retain(StateBackupKey, m.cfg.BackupRetain)
	m.emit("state.backed_up", "", "", b.ID)
	return b, nil
}

// BackupProject snapshots every workload in a project (generic across workload
// types — a RapidNative project's runners and its tinbase backend alike).
func (m *Manager) BackupProject(ctx context.Context, projectID string) ([]backup.Backup, error) {
	if m.backupStore() == nil {
		return nil, ErrBackupsDisabled
	}
	if _, err := m.store.GetProject(projectID); err != nil {
		return nil, err
	}
	var out []backup.Backup
	var firstErr error
	for _, w := range m.store.ListWorkloads(projectID) {
		b, err := m.BackupWorkload(ctx, w.ID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			log.Printf("backup %s: %v", w.ID, err)
			continue
		}
		out = append(out, b)
	}
	return out, firstErr
}

// ListBackups lists backups for a workload ("" = every workload).
func (m *Manager) ListBackups(workloadID string) ([]backup.Backup, error) {
	bk := m.backupStore()
	if bk == nil {
		return nil, ErrBackupsDisabled
	}
	return bk.List(workloadID)
}

// RestoreWorkload replaces a workload's data with a backup and boots it.
func (m *Manager) RestoreWorkload(ctx context.Context, workloadID, backupID string) error {
	bk := m.backupStore()
	if bk == nil {
		return ErrBackupsDisabled
	}
	w, err := m.store.GetWorkload(workloadID)
	if err != nil {
		return err
	}
	rl := m.refLock(workloadID)
	rl.Lock()
	defer rl.Unlock()

	_ = m.rt.Stop(ctx, workloadID) // remove container so we can replace the volume
	m.forget(workloadID)

	m.reclaimPath(w.DataDir) // wipe current data (guarded within the data root)
	if err := os.MkdirAll(w.DataDir, 0o700); err != nil {
		return err
	}
	if err := bk.Restore(backupID, w.DataDir); err != nil {
		_ = m.store.SetWorkloadState(workloadID, runtime.StateFailed)
		return fmt.Errorf("restore: %w", err)
	}
	inst, err := m.rt.Start(ctx, m.specFor(w))
	if err != nil {
		_ = m.store.SetWorkloadState(workloadID, runtime.StateFailed)
		return fmt.Errorf("boot after restore: %w", err)
	}
	_ = m.store.SetWorkloadState(workloadID, runtime.StateRunning)
	m.markLive(workloadID, inst.Addr)
	m.emit("backup.restored", w.ProjectID, workloadID, backupID)
	return nil
}

// DeleteBackup removes a single backup.
func (m *Manager) DeleteBackup(id string) error {
	bk := m.backupStore()
	if bk == nil {
		return ErrBackupsDisabled
	}
	return bk.Delete(id)
}

// RunBackupScheduler auto-backs up tinbase workloads on the configured interval.
func (m *Manager) RunBackupScheduler(ctx context.Context) {
	if m.backupStore() == nil || m.cfg.BackupInterval <= 0 {
		return
	}
	t := time.NewTicker(m.cfg.BackupInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.backupAll(ctx)
		}
	}
}

func (m *Manager) backupAll(ctx context.Context) {
	// Generic: back up every workload of every project (a project's runners and
	// its tinbase backend alike), not just tinbase.
	for _, w := range m.store.ListWorkloads("") {
		if b, err := m.BackupWorkload(ctx, w.ID); err != nil {
			log.Printf("backup %s: %v", w.ID, err)
		} else {
			log.Printf("backup %s -> %s (%d bytes)", w.ID, b.ID, b.SizeBytes)
		}
	}
	// Also snapshot the control-plane index itself, off-box, on the same schedule.
	if b, err := m.BackupState(ctx); err != nil {
		log.Printf("backup state: %v", err)
	} else {
		log.Printf("backup state -> %s (%d bytes)", b.ID, b.SizeBytes)
	}
}

// keyFor builds the route key for a workload: <ref> for the primary, else
// <ref>-<name>. Used for both subdomain and subroute addressing.
func (m *Manager) keyFor(ref, name string) string {
	if name == "" {
		return ref
	}
	return ref + "-" + name
}

// allocHostPort assigns the next free stable port for port-per-workload
// addressing, counting up from cfg.PortBase and skipping ports already reserved
// by other workloads or currently in use. Returns 0 when port addressing is off
// (the gateway/subdomain model).
func (m *Manager) allocHostPort() int {
	if m.cfg.PortBase <= 0 {
		return 0
	}
	m.portMu.Lock()
	defer m.portMu.Unlock()
	used := map[int]bool{}
	for _, w := range m.store.ListWorkloads("") {
		if w.HostPort > 0 {
			used[w.HostPort] = true
		}
	}
	for p := m.cfg.PortBase; p < m.cfg.PortBase+2000; p++ {
		if !used[p] && portFree(p) {
			return p
		}
	}
	log.Printf("port-alloc: no free port in [%d, %d)", m.cfg.PortBase, m.cfg.PortBase+2000)
	return 0
}

// portFree reports whether a TCP port on localhost can be bound right now.
func portFree(p int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

func (m *Manager) specFor(w *store.Workload) runtime.Spec {
	// Place the workload on its region's Docker host (a worker node); empty = local.
	dockerHost := ""
	var projEnv map[string]string
	if p, err := m.store.GetProject(w.ProjectID); err == nil {
		projEnv = p.Env
		if rg, err := m.store.GetRegion(p.Region); err == nil {
			dockerHost = rg.DockerHost
		}
	}
	// Precedence: platform env, then project env, then per-workload env (most specific wins).
	env := map[string]string{"TINBASE_JWT_SECRET": w.JWTSecret}
	for k, v := range projEnv {
		env[k] = v
	}
	for k, v := range w.Env {
		env[k] = v
	}
	return runtime.Spec{
		Type:        w.Type,
		Ref:         w.ID, // runtime key = workload id
		DataDir:     w.DataDir,
		Image:       w.Image,
		Port:        w.Port,
		HostPort:    w.HostPort,
		TemplateSrc: m.templatePath(w.Template),
		Workspace:   w.Workspace,
		DockerHost:  dockerHost,
		Env:         env,
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
