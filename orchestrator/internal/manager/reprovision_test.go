package manager

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

func newReprovisionTestManager(t *testing.T, hookURL string, hold time.Duration) (*Manager, store.Store) {
	t.Helper()
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSettings(store.Settings{Webhook: store.Webhook{URL: hookURL}}); err != nil {
		t.Fatal(err)
	}
	m := New(config.Config{DataRoot: t.TempDir(), ReprovisionHookTimeout: hold}, st, reapStub{})
	return m, st
}

// The receiver registers the route within its handler and then keeps the
// response open (files, convention routes). The hold must end as soon as the
// route exists, not when the response finally arrives.
func TestRequestReprovision_ReturnsOnceRouteAppearsWhileHookInFlight(t *testing.T) {
	const host = "p1.example.test"
	release := make(chan struct{})
	var m *Manager
	var st store.Store
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = st.PutProject(&store.Project{ID: "p1"})
		_ = st.PutWorkload(&store.Workload{ID: "w1", ProjectID: "p1"})
		if err := m.AddRoute(host, "w1"); err != nil {
			t.Errorf("AddRoute: %v", err)
		}
		<-release // response stays open well past the hold window
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(release)

	m, st = newReprovisionTestManager(t, srv.URL, 3*time.Second)

	start := time.Now()
	wl, err := m.RequestReprovision(context.Background(), host)
	if err != nil {
		t.Fatalf("RequestReprovision: %v", err)
	}
	if wl == nil || wl.ID != "w1" {
		t.Fatalf("got workload %+v, want w1", wl)
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("returned after %v; should return as soon as the route exists, not wait for the hook response", d)
	}
}

func TestRequestReprovision_HookRejectsIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	m, _ := newReprovisionTestManager(t, srv.URL, 3*time.Second)

	_, err := m.RequestReprovision(context.Background(), "nobody.example.test")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("got %v, want store.ErrNotFound", err)
	}
}

func TestRequestReprovision_TimesOutWhenRouteNeverAppears(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(release)
	m, _ := newReprovisionTestManager(t, srv.URL, 400*time.Millisecond)

	_, err := m.RequestReprovision(context.Background(), "slow.example.test")
	if !errors.Is(err, ErrReprovisionTimeout) {
		t.Fatalf("got %v, want ErrReprovisionTimeout", err)
	}
}
