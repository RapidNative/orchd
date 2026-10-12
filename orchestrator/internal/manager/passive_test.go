package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// passiveStub reports a settable state and counts wakes (Start).
type passiveStub struct {
	reapStub
	state  runtime.State
	starts int
}

func (s *passiveStub) Start(context.Context, runtime.Spec) (*runtime.Instance, error) {
	s.starts++
	s.state = runtime.StateRunning
	return &runtime.Instance{State: runtime.StateRunning, Addr: "127.0.0.1:1"}, nil
}
func (s *passiveStub) Status(context.Context, string) (runtime.State, error) { return s.state, nil }

func newPassiveMgr(t *testing.T) (*Manager, *passiveStub) {
	t.Helper()
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	rt := &passiveStub{state: runtime.StateRunning}
	m := New(config.Config{IdleTimeout: time.Minute, LRUActivityCoalesce: time.Hour, DataRoot: t.TempDir()}, st, rt)
	_ = st.PutProject(&store.Project{ID: "p"})
	_ = st.PutWorkload(&store.Workload{ID: "w", ProjectID: "p", State: runtime.StateRunning})
	m.markLive("w", "127.0.0.1:1")
	m.mu.Lock()
	m.live["w"].lastSeen = time.Now().Add(-time.Hour)
	m.mu.Unlock()
	return m, rt
}

func TestRunningAddrDoesNotCountAsUse(t *testing.T) {
	m, rt := newPassiveMgr(t)
	seen := m.live["w"].lastSeen

	addr, err := m.RunningAddr(context.Background(), "w")
	if err != nil || addr != "127.0.0.1:1" {
		t.Fatalf("RunningAddr = %q, %v; want the live address", addr, err)
	}
	m.mu.Lock()
	touched := !m.live["w"].lastSeen.Equal(seen)
	_, clocked := m.activityPersist["p"]
	m.mu.Unlock()
	if touched || clocked {
		t.Fatalf("the lookup counted as use (idle timer reset: %v, LRU clock noted: %v)", touched, clocked)
	}
	// So the reaper still sees an hour of idleness.
	m.ReapIdle(context.Background())
	if isLive(m, "w") {
		t.Fatal("an idle workload stayed awake after a RunningAddr lookup")
	}
	if rt.starts != 0 {
		t.Fatalf("RunningAddr started the workload %d times", rt.starts)
	}
}

func TestRunningAddrNeverWakes(t *testing.T) {
	ctx := context.Background()

	m, rt := newPassiveMgr(t)
	m.forget("w")
	rt.state = runtime.StateSuspended
	if _, err := m.RunningAddr(ctx, "w"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("suspended workload: err = %v, want ErrNotRunning", err)
	}
	if rt.starts != 0 || isLive(m, "w") {
		t.Fatal("RunningAddr woke a suspended workload")
	}

	// A live entry the runtime no longer backs (stopped out of band).
	m, rt = newPassiveMgr(t)
	rt.state = runtime.StateSuspended
	if _, err := m.RunningAddr(ctx, "w"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stale live entry: err = %v, want ErrNotRunning", err)
	}

	if _, err := m.RunningAddr(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown workload: err = %v, want store.ErrNotFound", err)
	}
}

// A wake or suspend holds the workload's lock; a lookup doesn't wait for it.
func TestRunningAddrDoesNotWaitForAWake(t *testing.T) {
	m, _ := newPassiveMgr(t)
	rl := m.refLock("w")
	rl.Lock()
	defer rl.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := m.RunningAddr(context.Background(), "w")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRunning) {
			t.Fatalf("mid-wake: err = %v, want ErrNotRunning", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunningAddr waited on a wake in progress")
	}
}
