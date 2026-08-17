package manager

import (
	"context"
	"testing"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// reapStub is a no-op runtime for exercising the reaper without a real driver.
type reapStub struct{}

func (reapStub) Name() string                                         { return "stub" }
func (reapStub) Stats(context.Context, string) (runtime.Stats, error) { return runtime.Stats{}, nil }
func (reapStub) Logs(context.Context, string, int) (string, error)    { return "", nil }
func (reapStub) Create(context.Context, runtime.Spec) (*runtime.Instance, error) {
	return &runtime.Instance{}, nil
}
func (reapStub) Start(context.Context, runtime.Spec) (*runtime.Instance, error) {
	return &runtime.Instance{}, nil
}
func (reapStub) Suspend(context.Context, string) error { return nil }
func (reapStub) Stop(context.Context, string) error    { return nil }
func (reapStub) Status(context.Context, string) (runtime.State, error) {
	return runtime.StateRunning, nil
}

func newReapMgr(t *testing.T, idle time.Duration) *Manager {
	t.Helper()
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	m := New(config.Config{IdleTimeout: idle, DataRoot: t.TempDir()}, st, reapStub{})
	_ = st.PutProject(&store.Project{ID: "p"})
	_ = st.PutWorkload(&store.Workload{ID: "w", ProjectID: "p", State: runtime.StateRunning})
	m.markLive("w", "127.0.0.1:1")
	// Make it look long-idle.
	m.mu.Lock()
	m.live["w"].lastSeen = time.Now().Add(-time.Hour)
	m.mu.Unlock()
	return m
}

func isLive(m *Manager, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.live[id]
	return ok
}

func TestReapDisabledWhenIdleTimeoutZero(t *testing.T) {
	m := newReapMgr(t, 0)
	m.ReapIdle(context.Background())
	if !isLive(m, "w") {
		t.Fatal("workload was reaped despite IdleTimeout=0 (scale-to-zero should be off)")
	}
}

func TestReapSuspendsIdleWhenEnabled(t *testing.T) {
	m := newReapMgr(t, 50*time.Millisecond)
	m.ReapIdle(context.Background())
	if isLive(m, "w") {
		t.Fatal("idle workload should have been reaped with a positive IdleTimeout")
	}
}

// A workload that outlived the orchd process which started it has no live
// entry, so the map-driven pass cannot see it. It must still be reaped —
// after one idle interval of grace, and not before.
func TestReapAdoptsInstancesThatOutlivedRestart(t *testing.T) {
	m := newReapMgr(t, time.Minute)
	m.forget("w") // what a restart leaves behind: running in the store, not live

	m.ReapIdle(context.Background())
	if w, _ := m.store.GetWorkload("w"); w.State != runtime.StateRunning {
		t.Fatal("reaped on first sight; an in-use workload must get its grace interval")
	}

	m.mu.Lock()
	m.unowned["w"] = time.Now().Add(-time.Hour)
	m.mu.Unlock()
	m.ReapIdle(context.Background())
	if w, _ := m.store.GetWorkload("w"); w.State != runtime.StateSuspended {
		t.Fatalf("still %s after the grace interval; want suspended", w.State)
	}
}

// A request during the grace interval re-arms the workload: it goes back to
// being tracked normally and the stale grace timestamp must not survive.
func TestGraceResetsWhenWorkloadIsUsedAgain(t *testing.T) {
	m := newReapMgr(t, time.Minute)
	m.forget("w")
	m.ReapIdle(context.Background())

	m.markLive("w", "127.0.0.1:1")
	m.mu.Lock()
	_, pending := m.unowned["w"]
	m.mu.Unlock()
	if pending {
		t.Fatal("grace timer survived a wake; the workload would be reaped while in use")
	}
}

// A wake must preempt an in-flight suspend rather than queue behind its
// multi-GB snapshot: EnsureRunning tells the runtime to cancel any suspend for
// the workload BEFORE taking the workload lock.
type preemptStub struct {
	reapStub
	preempted []string
}

func (p *preemptStub) PreemptSuspend(ref string) { p.preempted = append(p.preempted, ref) }

func TestEnsureRunningPreemptsInFlightSuspend(t *testing.T) {
	st, _ := store.Open("")
	stub := &preemptStub{}
	m := New(config.Config{DataRoot: t.TempDir()}, st, stub)
	_ = st.PutProject(&store.Project{ID: "p"})
	_ = st.PutWorkload(&store.Workload{ID: "w", ProjectID: "p", State: runtime.StateRunning})

	if _, err := m.EnsureRunning(context.Background(), "w"); err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if len(stub.preempted) != 1 || stub.preempted[0] != "w" {
		t.Fatalf("wake must preempt the workload's suspend first, got %v", stub.preempted)
	}
}

// Preemption is a success outcome for the reaper: the workload must stay
// running, not be marked suspended or logged as a failure.
type preemptedSuspendStub struct{ reapStub }

func (preemptedSuspendStub) Suspend(context.Context, string) error {
	return runtime.ErrSuspendPreempted
}

func TestReapTreatsPreemptionAsWakeWin(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{IdleTimeout: time.Minute, DataRoot: t.TempDir()}, st, preemptedSuspendStub{})
	_ = st.PutProject(&store.Project{ID: "p"})
	_ = st.PutWorkload(&store.Workload{ID: "w", ProjectID: "p", State: runtime.StateRunning})
	m.markLive("w", "127.0.0.1:1")
	m.mu.Lock()
	m.live["w"].lastSeen = time.Now().Add(-time.Hour)
	m.mu.Unlock()

	m.ReapIdle(context.Background())
	if w, _ := st.GetWorkload("w"); w.State != runtime.StateRunning {
		t.Fatalf("preempted suspend must leave the workload running, got %s", w.State)
	}
}
