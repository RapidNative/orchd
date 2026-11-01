package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/manager"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// upstreamRuntime runs every workload at one real HTTP server, always running.
type upstreamRuntime struct{ addr string }

func (upstreamRuntime) Name() string { return "upstream" }
func (upstreamRuntime) Stats(context.Context, string) (runtime.Stats, error) {
	return runtime.Stats{}, nil
}
func (upstreamRuntime) Logs(context.Context, string, int) (string, error) { return "", nil }
func (u upstreamRuntime) Create(_ context.Context, s runtime.Spec) (*runtime.Instance, error) {
	return &runtime.Instance{Ref: s.Ref, State: runtime.StateRunning, Addr: u.addr}, nil
}
func (u upstreamRuntime) Start(_ context.Context, s runtime.Spec) (*runtime.Instance, error) {
	return &runtime.Instance{Ref: s.Ref, State: runtime.StateRunning, Addr: u.addr}, nil
}
func (upstreamRuntime) Suspend(context.Context, string) error { return nil }
func (upstreamRuntime) Stop(context.Context, string) error    { return nil }
func (upstreamRuntime) Status(context.Context, string) (runtime.State, error) {
	return runtime.StateRunning, nil
}

// newTestGateway routes app.test.local to workload "w" of project "p" (named
// name), served by upstream.
func newTestGateway(tb testing.TB, upstream http.Handler, name string) (*Gateway, store.Store) {
	tb.Helper()
	up := httptest.NewServer(upstream)
	tb.Cleanup(up.Close)
	cfg := config.Load()
	cfg.DataRoot = tb.TempDir()
	cfg.BaseDomain = "test.local"
	cfg.StartupWait = 0
	st, err := store.Open("")
	if err != nil {
		tb.Fatal(err)
	}
	mgr := manager.New(cfg, st, upstreamRuntime{addr: strings.TrimPrefix(up.URL, "http://")})
	_ = st.PutProject(&store.Project{ID: "p", Name: name})
	_ = st.PutWorkload(&store.Workload{ID: "w", ProjectID: "p", State: runtime.StateSuspended})
	_ = st.PutRoute(&store.Route{Host: "app.test.local", Key: "w", WorkloadID: "w"})
	return New(mgr, cfg), st
}
