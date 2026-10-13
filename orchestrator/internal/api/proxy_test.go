package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/api"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/manager"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// upstreamRuntime runs its workload at a real HTTP server, keeps a settable
// state, and counts wakes (Start).
type upstreamRuntime struct {
	stubRuntime
	addr   string
	mu     sync.Mutex
	state  runtime.State
	starts int
}

func (u *upstreamRuntime) Start(context.Context, runtime.Spec) (*runtime.Instance, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.starts++
	u.state = runtime.StateRunning
	return &runtime.Instance{State: runtime.StateRunning, Addr: u.addr}, nil
}
func (u *upstreamRuntime) Suspend(context.Context, string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.state = runtime.StateSuspended
	return nil
}
func (u *upstreamRuntime) Status(context.Context, string) (runtime.State, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state, nil
}
func (u *upstreamRuntime) wakes() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.starts
}

// seenRequest is what the workload received.
type seenRequest struct {
	path, query              string
	authorization, apiKey    string
	forwardHeader, tenantKey string
}

// newProxyServer serves the API over a manager whose one workload, "w", is
// suspended and runs (once woken) at an upstream that records each request.
func newProxyServer(t *testing.T, idle time.Duration) (*httptest.Server, *manager.Manager, *upstreamRuntime, chan seenRequest) {
	t.Helper()
	seen := make(chan seenRequest, 16)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- seenRequest{
			path:          r.URL.Path,
			query:         r.URL.RawQuery,
			authorization: r.Header.Get("Authorization"),
			apiKey:        r.Header.Get("X-API-Key"),
			forwardHeader: r.Header.Get("X-Orchd-Forward-Authorization"),
			tenantKey:     r.Header.Get("apikey"),
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = io.WriteString(w, `{"users":[]}`)
	}))
	t.Cleanup(upstream.Close)

	cfg := config.Load()
	cfg.DataRoot = t.TempDir()
	cfg.BaseDomain = "test.local"
	cfg.IdleTimeout = idle
	st, err := store.Open("")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	rt := &upstreamRuntime{addr: strings.TrimPrefix(upstream.URL, "http://"), state: runtime.StateSuspended}
	mgr := manager.New(cfg, st, rt)
	_ = st.PutProject(&store.Project{ID: "p"})
	_ = st.PutWorkload(&store.Workload{ID: "w", ProjectID: "p", State: runtime.StateSuspended})

	srv := httptest.NewServer(api.New(mgr, cfg, bootstrapKey).Handler())
	t.Cleanup(srv.Close)
	return srv, mgr, rt, seen
}

func proxyGet(t *testing.T, srv *httptest.Server, path string, headers map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func TestProxyWorkloadForwardsWithoutTheAPIKey(t *testing.T) {
	srv, mgr, rt, seen := newProxyServer(t, time.Minute)
	if _, err := mgr.EnsureRunning(context.Background(), "w"); err != nil {
		t.Fatal(err)
	}

	code, body := proxyGet(t, srv, "/v1/workloads/w/proxy/auth/v1/admin/users?page=2", map[string]string{
		"Authorization":                 "Bearer " + bootstrapKey,
		"X-Orchd-Forward-Authorization": "Bearer tenant-token",
		"apikey":                        "tenant-key",
	})
	if code != http.StatusOK || body != `{"users":[]}` {
		t.Fatalf("proxy: %d %s", code, body)
	}
	got := <-seen
	want := seenRequest{path: "/auth/v1/admin/users", query: "page=2", authorization: "Bearer tenant-token", tenantKey: "tenant-key"}
	if got != want {
		t.Fatalf("the workload received %+v, want %+v", got, want)
	}

	// Without a forwarded Authorization, the workload gets none: never the API key.
	req, _ := http.NewRequest("GET", srv.URL+"/v1/workloads/w/proxy/", nil)
	req.Header.Set("X-API-Key", bootstrapKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := <-seen; got.authorization != "" || got.apiKey != "" || got.path != "/" {
		t.Fatalf("the workload received %+v; the API key must not reach it", got)
	}
	if v := res.Header.Values("Access-Control-Allow-Origin"); len(v) != 1 {
		t.Fatalf("Access-Control-Allow-Origin = %q, want the API's one value", v)
	}
	if rt.wakes() != 1 {
		t.Fatalf("wakes = %d, want only the EnsureRunning one", rt.wakes())
	}

	var info map[string]any
	_, raw := do(t, srv, "GET", "/v1/info", bootstrapKey, nil)
	if err := json.Unmarshal(raw, &info); err != nil || info["workload_proxy"] != true {
		t.Fatalf("/v1/info doesn't report workload_proxy: %s", raw)
	}
}

// Requests through the proxy leave the idle timer alone, so the reaper still
// suspends the workload, and once suspended the proxy doesn't wake it.
func TestProxyWorkloadDoesNotCountAsUse(t *testing.T) {
	const idle = 200 * time.Millisecond
	srv, mgr, rt, seen := newProxyServer(t, idle)
	ctx := context.Background()
	if _, err := mgr.EnsureRunning(ctx, "w"); err != nil {
		t.Fatal(err)
	}
	key := map[string]string{"Authorization": "Bearer " + bootstrapKey}

	// Polled well inside the idle timeout, the last poll 50ms before the reap.
	for i := 0; i < 5; i++ {
		time.Sleep(idle / 4)
		if code, body := proxyGet(t, srv, "/v1/workloads/w/proxy/health", key); code != http.StatusOK {
			t.Fatalf("poll %d: %d %s", i, code, body)
		}
		<-seen
	}
	time.Sleep(idle / 4)
	mgr.ReapIdle(ctx)
	if st, _ := rt.Status(ctx, "w"); st != runtime.StateSuspended {
		t.Fatal("polling through the proxy kept the workload awake")
	}

	code, _ := proxyGet(t, srv, "/v1/workloads/w/proxy/health", key)
	if code != http.StatusConflict {
		t.Fatalf("suspended workload: got %d, want 409", code)
	}
	if rt.wakes() != 1 {
		t.Fatalf("the proxy woke a suspended workload (wakes = %d)", rt.wakes())
	}
}

func TestProxyWorkloadNeedsTheKeyAndAWorkload(t *testing.T) {
	srv, _, _, _ := newProxyServer(t, time.Minute)
	if code, _ := proxyGet(t, srv, "/v1/workloads/w/proxy/", nil); code != http.StatusUnauthorized {
		t.Fatalf("no key: got %d, want 401", code)
	}
	code, _ := proxyGet(t, srv, "/v1/workloads/nope/proxy/", map[string]string{"Authorization": "Bearer " + bootstrapKey})
	if code != http.StatusNotFound {
		t.Fatalf("unknown workload: got %d, want 404", code)
	}
}
