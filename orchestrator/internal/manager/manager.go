// Package manager is the control-plane brain: it provisions projects, mints their
// credentials, and owns the wake/scale-to-zero lifecycle. The gateway calls
// EnsureRunning on every request; a background reaper suspends idle instances.
package manager

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
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
	live     map[string]*liveInstance // refs currently running, for idle tracking
	refLocks map[string]*sync.Mutex   // serialize wake per ref (avoid thundering herd)
}

type liveInstance struct {
	addr     string
	lastSeen time.Time
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

// CreateProject provisions a brand-new tinbase project: mint a JWT secret and
// keys, allocate a data dir, first-boot the instance, and record it.
func (m *Manager) CreateProject(ctx context.Context, wtype runtime.WorkloadType) (*store.Project, error) {
	ref, err := newRef()
	if err != nil {
		return nil, err
	}
	if wtype == "" {
		wtype = runtime.WorkloadTinbaseProject
	}

	secret, err := newSecret()
	if err != nil {
		return nil, err
	}
	dataDir := filepath.Join(m.cfg.DataRoot, "projects", ref)
	anon, svc := m.mintKeys(secret) // best-effort; empty if tinbase keys unavailable

	p := &store.Project{
		Ref:       ref,
		Type:      wtype,
		Region:    m.cfg.Region,
		State:     runtime.StateProvisioning,
		DataDir:   dataDir,
		JWTSecret: secret,
		AnonKey:   anon,
		SvcKey:    svc,
		CreatedAt: time.Now(),
	}
	if err := m.store.Put(p); err != nil {
		return nil, err
	}

	inst, err := m.rt.Create(ctx, m.specFor(p))
	if err != nil {
		_ = m.store.SetState(ref, runtime.StateFailed)
		return nil, fmt.Errorf("provision: %w", err)
	}
	_ = m.store.SetState(ref, runtime.StateRunning)
	m.markLive(ref, inst.Addr)

	p, _ = m.store.Get(ref)
	return p, nil
}

// EnsureRunning returns the address to proxy to for ref, waking (Start) a
// suspended/stopped instance on demand. This is the hot path the gateway hits on
// every request.
func (m *Manager) EnsureRunning(ctx context.Context, ref string) (string, error) {
	p, err := m.store.Get(ref)
	if err != nil {
		return "", err
	}

	rl := m.refLock(ref)
	rl.Lock()
	defer rl.Unlock()

	// Fast path: already tracked as live and the driver agrees.
	m.mu.Lock()
	li, ok := m.live[ref]
	m.mu.Unlock()
	if ok {
		if st, _ := m.rt.Status(ctx, ref); st == runtime.StateRunning {
			m.touch(ref)
			return li.addr, nil
		}
	}

	// Cold: bring it up. Create() and Start() converge for LocalDriver; a real
	// driver distinguishes first-provision from snapshot-resume.
	inst, err := m.rt.Start(ctx, m.specFor(p))
	if err != nil {
		return "", fmt.Errorf("wake %s: %w", ref, err)
	}
	_ = m.store.SetState(ref, runtime.StateRunning)
	m.markLive(ref, inst.Addr)
	return inst.Addr, nil
}

// Touch records activity so the reaper does not suspend a busy instance.
func (m *Manager) Touch(ref string) { m.touch(ref) }

// DeleteProject stops the instance and removes its record. Data-dir cleanup is
// left to a separate reclaim step so a delete is recoverable in v0.
func (m *Manager) DeleteProject(ctx context.Context, ref string) error {
	if _, err := m.store.Get(ref); err != nil {
		return err
	}
	_ = m.rt.Stop(ctx, ref)
	m.mu.Lock()
	delete(m.live, ref)
	m.mu.Unlock()
	return m.store.Delete(ref)
}

// ReapIdle suspends instances that have been idle longer than the configured
// timeout. Run it on a ticker.
func (m *Manager) ReapIdle(ctx context.Context) {
	now := time.Now()
	m.mu.Lock()
	var stale []string
	for ref, li := range m.live {
		if now.Sub(li.lastSeen) > m.cfg.IdleTimeout {
			stale = append(stale, ref)
		}
	}
	m.mu.Unlock()

	for _, ref := range stale {
		rl := m.refLock(ref)
		rl.Lock()
		if err := m.rt.Suspend(ctx, ref); err == nil {
			_ = m.store.SetState(ref, runtime.StateSuspended)
			m.mu.Lock()
			delete(m.live, ref)
			m.mu.Unlock()
		}
		rl.Unlock()
	}
}

// RunReaper drives ReapIdle until ctx is cancelled.
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

func (m *Manager) specFor(p *store.Project) runtime.Spec {
	return runtime.Spec{
		Type:    p.Type,
		Ref:     p.Ref,
		DataDir: p.DataDir,
		WorkDir: p.WorkDir,
		Env: map[string]string{
			"TINBASE_JWT_SECRET": p.JWTSecret,
		},
	}
}

func (m *Manager) markLive(ref, addr string) {
	m.mu.Lock()
	m.live[ref] = &liveInstance{addr: addr, lastSeen: time.Now()}
	m.mu.Unlock()
}

func (m *Manager) touch(ref string) {
	m.mu.Lock()
	if li, ok := m.live[ref]; ok {
		li.lastSeen = time.Now()
	}
	m.mu.Unlock()
}

func (m *Manager) refLock(ref string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.refLocks[ref]
	if !ok {
		l = &sync.Mutex{}
		m.refLocks[ref] = l
	}
	return l
}

// mintKeys derives the anon/service_role keys for a JWT secret by asking tinbase.
// Best-effort: if the binary or subcommand is unavailable, keys are left empty
// and can be fetched from the running instance later.
func (m *Manager) mintKeys(secret string) (anon, svc string) {
	// Hard timeout so a misbehaving key-mint can never hang provisioning.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch {
	case m.cfg.Driver == "docker":
		// No local tinbase binary on a container host; mint via the image.
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
	// 10 lowercase alphanumerics, DNS-label safe, collision-resistant enough for v0.
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	// Ensure it starts with a letter (valid DNS label).
	if b[0] >= '0' && b[0] <= '9' {
		b[0] = 'a'
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
