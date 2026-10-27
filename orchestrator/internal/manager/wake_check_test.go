package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// wakeTestManager: template "app" has images v1 and v2; workloads "w" (mobile)
// and "api" of project "p" sleep on v1. The receiver is handler.
func wakeTestManager(t *testing.T, timeout time.Duration, handler http.HandlerFunc) (*Manager, store.Store, *runtime.MockDriver) {
	t.Helper()
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	if err := st.SetSettings(store.Settings{Webhook: store.Webhook{URL: srv.URL, APIKey: "k"}}); err != nil {
		t.Fatal(err)
	}
	rt := runtime.NewMockDriver()
	m := New(config.Config{DataRoot: t.TempDir(), WakeHookTimeout: timeout}, st, rt)
	for _, v := range []string{"v1", "v2"} {
		_ = st.PutImage(&store.Image{
			Template:  "app",
			Version:   v,
			Dockers:   map[string]string{"mobile": "app-mobile:" + v, "api": "app-api:" + v},
			Workloads: []store.ImageWorkload{{Kind: "node", Workspace: "mobile"}, {Name: "api", Kind: "node", Workspace: "api"}},
		})
	}
	_ = st.PutProject(&store.Project{ID: "p"})
	for _, id := range []string{"w", "api"} {
		ws := map[string]string{"w": "mobile", "api": "api"}[id]
		_ = st.PutWorkload(&store.Workload{ID: id, ProjectID: "p", Type: runtime.WorkloadRapidNativeDev, Template: "app", ImageVer: "v1", Workspace: ws, DataDir: t.TempDir()})
	}
	return m, st, rt
}

func TestWakeCheck_AsksForABehindWorkloadAndWakesOnWhatTheReceiverSet(t *testing.T) {
	var mu sync.Mutex // the race detector doesn't see the ordering the HTTP round trip gives
	var got imageBehindEvent
	var m *Manager
	m, st, _ := wakeTestManager(t, 5*time.Second, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Webhook-Key") != "k" {
			t.Errorf("missing webhook key")
		}
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&got)
		mu.Unlock()
		// The receiver moves the workload while it sleeps, as the website does.
		if _, err := m.SetWorkloadImage(context.Background(), "w", "v2"); err != nil {
			t.Errorf("SetWorkloadImage: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})

	m.CheckImageBeforeWake(context.Background(), "w")
	want := imageBehindEvent{Type: "workload.image_behind", ProjectID: "p", WorkloadID: "w", Template: "app", ImageVersion: "v1", LatestVersion: "v2"}
	mu.Lock()
	defer mu.Unlock()
	if got != want {
		t.Fatalf("event = %+v, want %+v", got, want)
	}
	if _, err := m.EnsureRunning(context.Background(), "w"); err != nil {
		t.Fatal(err)
	}
	w, _ := st.GetWorkload("w")
	if w.ImageVer != "v2" {
		t.Fatalf("woke on %s, want v2", w.ImageVer)
	}
}

func TestWakeCheck_SilentForACurrentRunningOrUnimagedWorkload(t *testing.T) {
	var calls atomic.Int32
	m, st, _ := wakeTestManager(t, 5*time.Second, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })

	// current
	cur, _ := st.GetWorkload("w")
	cur.ImageVer = "v2"
	_ = st.PutWorkload(cur)
	m.CheckImageBeforeWake(context.Background(), "w")

	// running (on an old image: a running one is moved by SetWorkloadImage's restart, not here)
	if _, err := m.EnsureRunning(context.Background(), "api"); err != nil {
		t.Fatal(err)
	}
	m.CheckImageBeforeWake(context.Background(), "api")

	// not booted from an image
	_ = st.PutWorkload(&store.Workload{ID: "db", ProjectID: "p", Type: runtime.WorkloadTinbaseProject, DataDir: t.TempDir()})
	m.CheckImageBeforeWake(context.Background(), "db")

	if n := calls.Load(); n != 0 {
		t.Fatalf("receiver called %d times, want 0", n)
	}
}

func TestWakeCheck_OneCallPerProjectForConcurrentWakesAndWithinTheCooldown(t *testing.T) {
	var calls atomic.Int32
	m, _, _ := wakeTestManager(t, 5*time.Second, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(200 * time.Millisecond)
	})
	var wg sync.WaitGroup
	for _, id := range []string{"w", "api", "w"} {
		wg.Add(1)
		go func(id string) { defer wg.Done(); m.CheckImageBeforeWake(context.Background(), id) }(id)
	}
	wg.Wait()
	// The receiver left it behind (e.g. it declined): the next wake doesn't ask again.
	m.CheckImageBeforeWake(context.Background(), "w")
	if n := calls.Load(); n != 1 {
		t.Fatalf("receiver called %d times, want 1", n)
	}
}

func TestWakeCheck_ASlowReceiverNeverHoldsTheWakePastTheTimeout(t *testing.T) {
	release := make(chan struct{})
	m, _, _ := wakeTestManager(t, 300*time.Millisecond, func(w http.ResponseWriter, r *http.Request) { <-release })
	defer close(release)
	start := time.Now()
	m.CheckImageBeforeWake(context.Background(), "w")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("held the wake for %v, want about the 300ms timeout", d)
	}
	if _, err := m.EnsureRunning(context.Background(), "w"); err != nil {
		t.Fatalf("wake after a slow receiver: %v", err)
	}
}

func TestWakeCheck_OffWhenTimeoutIsZeroOrNoWebhook(t *testing.T) {
	var calls atomic.Int32
	m, _, _ := wakeTestManager(t, 0, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	m.CheckImageBeforeWake(context.Background(), "w")

	m2, st2, _ := wakeTestManager(t, time.Second, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	_ = st2.SetSettings(store.Settings{})
	m2.CheckImageBeforeWake(context.Background(), "w")
	if n := calls.Load(); n != 0 {
		t.Fatalf("receiver called %d times, want 0", n)
	}
}

// statusCounter counts runtime status calls, which the gateway's per-request
// check must not make.
type statusCounter struct {
	runtime.Runtime
	n atomic.Int32
}

func (s *statusCounter) Status(ctx context.Context, ref string) (runtime.State, error) {
	s.n.Add(1)
	return s.Runtime.Status(ctx, ref)
}

func TestWakeCheck_ALiveWorkloadCostsNoRuntimeCall(t *testing.T) {
	var calls atomic.Int32
	m, _, rt := wakeTestManager(t, 5*time.Second, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	sc := &statusCounter{Runtime: rt}
	m.rt = sc
	// "api" is behind (v1) and running: a request to it must not ask the runtime or the receiver.
	if _, err := m.EnsureRunning(context.Background(), "api"); err != nil {
		t.Fatal(err)
	}
	before := sc.n.Load()
	for i := 0; i < 50; i++ {
		m.CheckImageBeforeWake(context.Background(), "api")
	}
	if n := sc.n.Load() - before; n != 0 {
		t.Fatalf("%d runtime status calls for 50 requests, want 0", n)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("receiver called %d times, want 0", n)
	}
}

// ageWakeChecks moves every remembered check past the cooldown.
func ageWakeChecks(m *Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.wakeChecks {
		c.at = time.Now().Add(-2 * wakeCheckCooldown)
	}
}

func TestWakeCheck_ADeclineIsRememberedUntilANewerImage(t *testing.T) {
	var calls atomic.Int32
	m, st, _ := wakeTestManager(t, 5*time.Second, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	m.CheckImageBeforeWake(context.Background(), "w") // answered, left on v1
	ageWakeChecks(m)
	m.CheckImageBeforeWake(context.Background(), "w")
	if n := calls.Load(); n != 1 {
		t.Fatalf("receiver called %d times after a decline and the cooldown, want 1", n)
	}
	// A newer image is a new question.
	_ = st.PutImage(&store.Image{Template: "app", Version: "v3", Dockers: map[string]string{"mobile": "app-mobile:v3"}})
	m.CheckImageBeforeWake(context.Background(), "w")
	if n := calls.Load(); n != 2 {
		t.Fatalf("receiver called %d times after a newer image, want 2", n)
	}
}

func TestWakeCheck_AFailedCallIsRetriedAfterTheCooldown(t *testing.T) {
	var calls atomic.Int32
	m, _, _ := wakeTestManager(t, 5*time.Second, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	m.CheckImageBeforeWake(context.Background(), "w")
	m.CheckImageBeforeWake(context.Background(), "w") // within the cooldown
	ageWakeChecks(m)
	m.CheckImageBeforeWake(context.Background(), "w")
	if n := calls.Load(); n != 2 {
		t.Fatalf("receiver called %d times, want 2 (once, then once after the cooldown)", n)
	}
}
