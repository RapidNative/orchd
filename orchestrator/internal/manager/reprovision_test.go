package manager

import (
	"context"
	"encoding/json"
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

func TestRequestReprovision_RemembersAHostTheReceiverDoesNotKnow(t *testing.T) {
	const host = "gone.example.test"
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body reprovisionRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Type == "domain.not_found" { // route events go to the same URL
			calls++
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	m, st := newReprovisionTestManager(t, srv.URL, 3*time.Second)

	for i := 0; i < 3; i++ { // one page load: many requests for the same host
		if _, err := m.RequestReprovision(context.Background(), host); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Fatalf("hook called %d times for one unknown host, want 1", calls)
	}

	// A project created for it meanwhile is served at once.
	_ = st.PutProject(&store.Project{ID: "p1"})
	_ = st.PutWorkload(&store.Workload{ID: "w1", ProjectID: "p1"})
	if err := m.AddRoute(host, "w1"); err != nil {
		t.Fatal(err)
	}
	if wl, err := m.RequestReprovision(context.Background(), host); err != nil || wl.ID != "w1" {
		t.Fatalf("a host that gained a route: %v, %v", wl, err)
	}
	_ = m.RemoveRoute(host)

	// After the TTL the receiver is asked again.
	m.mu.Lock()
	m.unknownHosts[host] = time.Now().Add(-unknownHostTTL)
	m.mu.Unlock()
	_, _ = m.RequestReprovision(context.Background(), host)
	if calls != 2 {
		t.Fatalf("hook calls after the TTL: %d, want 2", calls)
	}
}

// Only a 404 is remembered; a receiver that failed is asked again next time.
func TestRequestReprovision_DoesNotRememberAFailure(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	m, _ := newReprovisionTestManager(t, srv.URL, 3*time.Second)
	for i := 0; i < 2; i++ {
		_, _ = m.RequestReprovision(context.Background(), "flaky.example.test")
	}
	if calls != 2 {
		t.Fatalf("hook calls after a 500: %d, want 2", calls)
	}
}
