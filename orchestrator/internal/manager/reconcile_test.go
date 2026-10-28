package manager

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// lifecycleRT records lifecycle calls, and runs the instances in up.
type lifecycleRT struct {
	reapStub
	mu    sync.Mutex
	calls []string
	up    map[string]bool
}

func (r *lifecycleRT) note(c string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *lifecycleRT) Status(_ context.Context, id string) (runtime.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.up[id] {
		return runtime.StateRunning, nil
	}
	return runtime.StateSuspended, nil
}

func (r *lifecycleRT) Suspend(_ context.Context, id string) error {
	r.note("suspend")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.up[id] = false
	return nil
}

func (r *lifecycleRT) Stop(_ context.Context, id string) error {
	r.note("stop")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.up[id] = false
	return nil
}

func (r *lifecycleRT) Create(_ context.Context, spec runtime.Spec) (*runtime.Instance, error) {
	r.note("create")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.up[spec.Ref] = true
	return &runtime.Instance{}, nil
}

func (r *lifecycleRT) RunningRefs(context.Context) (map[string]bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]bool{}
	for id, up := range r.up {
		if up {
			out[id] = true
		}
	}
	return out, nil
}

func newLifecycleMgr(t *testing.T) (*Manager, *lifecycleRT) {
	t.Helper()
	m := newReapMgr(t, time.Minute)
	rt := &lifecycleRT{up: map[string]bool{"w": true}}
	m.rt = rt
	return m, rt
}

func TestRestartShutsDownBeforeRemoving(t *testing.T) {
	m, rt := newLifecycleMgr(t)
	if err := m.RestartWorkload(context.Background(), "w"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rt.calls, ","); got != "suspend,stop,create" {
		t.Fatalf("restart ran %s, want a graceful stop before the removal", got)
	}

	// A workload that is already down has nothing to shut down.
	rt.up["w"], rt.calls = false, nil
	if err := m.RestartWorkload(context.Background(), "w"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rt.calls, ","); got != "stop,create" {
		t.Fatalf("restart of a stopped workload ran %s", got)
	}
}

// What staging had: a database the store called suspended while its container
// ran for a day, so nothing ever reaped or backed it up.
func TestReconcileAdoptsARunningInstanceTheStoreCallsDown(t *testing.T) {
	m, rt := newLifecycleMgr(t)
	m.forget("w")
	_ = m.store.SetWorkloadState("w", runtime.StateSuspended)
	_ = m.store.PutWorkload(&store.Workload{ID: "failed", ProjectID: "p", State: runtime.StateFailed})
	_ = m.store.PutWorkload(&store.Workload{ID: "asleep", ProjectID: "p", State: runtime.StateSuspended})
	rt.up["failed"] = true

	if n := m.reconcileStates(context.Background()); n != 2 {
		t.Fatalf("reconciled %d, want 2", n)
	}
	for id, want := range map[string]runtime.State{"w": runtime.StateRunning, "failed": runtime.StateRunning, "asleep": runtime.StateSuspended} {
		if w, _ := m.store.GetWorkload(id); w.State != want {
			t.Errorf("%s is %s, want %s", id, w.State, want)
		}
	}

	// From there the reaper treats it as an instance it does not own: one grace
	// interval, then a normal suspend.
	m.ReapIdle(context.Background())
	m.mu.Lock()
	m.unowned["w"] = time.Now().Add(-time.Hour)
	m.mu.Unlock()
	m.ReapIdle(context.Background())
	if w, _ := m.store.GetWorkload("w"); w.State != runtime.StateSuspended || rt.up["w"] {
		t.Fatalf("after the grace interval: %s, up %v", w.State, rt.up["w"])
	}
}

func TestReconcileLeavesAWorkloadMidLifecycle(t *testing.T) {
	m, _ := newLifecycleMgr(t)
	_ = m.store.SetWorkloadState("w", runtime.StateSuspended)
	rl := m.refLock("w")
	rl.Lock() // a wake under way; it sets the state itself
	n := m.reconcileStates(context.Background())
	rl.Unlock()
	if w, _ := m.store.GetWorkload("w"); n != 0 || w.State != runtime.StateSuspended {
		t.Fatalf("reconciled a workload mid-wake: %d, %s", n, w.State)
	}
}
