package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// recordingRuntime captures every Spec passed to Create/Start so tests can
// assert what env a workload would actually boot with.
type recordingRuntime struct {
	reapStub
	mu    sync.Mutex
	specs []runtime.Spec
}

func (r *recordingRuntime) Create(_ context.Context, s runtime.Spec) (*runtime.Instance, error) {
	r.mu.Lock()
	r.specs = append(r.specs, s)
	r.mu.Unlock()
	return &runtime.Instance{Ref: s.Ref, Addr: "127.0.0.1:1"}, nil
}

func (r *recordingRuntime) specFor(ref string) (runtime.Spec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.specs {
		if s.Ref == ref {
			return s, true
		}
	}
	return runtime.Spec{}, false
}

// waitSpecs polls until n specs have been recorded (provisioning is async).
func (r *recordingRuntime) waitSpecs(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := len(r.specs)
		r.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d provisioned specs", n)
}

func newInterpManager(t *testing.T, rt runtime.Runtime) *Manager {
	t.Helper()
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DataRoot: t.TempDir(), BaseDomain: "test.local", GatewayAddr: ":8091"}
	return New(cfg, st, rt)
}

// TestInterpolateEnvTokens is the pure resolution table: each supported token
// against a project with a tinbase primary ("db") and a named "api" workload.
func TestInterpolateEnvTokens(t *testing.T) {
	m := newInterpManager(t, reapStub{})
	proj, wls, err := m.CreateProject(context.Background(), "demo", "", []WorkloadSpec{
		{Type: runtime.WorkloadTinbaseProject, Workspace: "db"},
		{Type: runtime.WorkloadRapidNativeDev, Name: "api", Workspace: "api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	db, api := wls[0], wls[1]
	// Key minting shells out to tinbase (absent in unit tests) — seed known
	// keys directly so the credential tokens have real values to resolve.
	db.AnonKey, db.SvcKey = "anon-key-123", "svc-key-456"
	if err := m.store.PutWorkload(db); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		in, want string
	}{
		{"${route.api}", "http://" + proj.ID + "-api.test.local:8091"},
		{"${route.db}", "http://" + proj.ID + ".test.local:8091"}, // primary: bare ref
		{"${host.api}", proj.ID + "-api.test.local"},
		{"${workload.db.anon_key}", db.AnonKey},
		{"${workload.db.service_key}", db.SvcKey},
		{"${workload.db.jwt_secret}", db.JWTSecret},
		{"${project.ref}", proj.ID},
		{"${base_domain}", "test.local"},
		{"prefix-${project.ref}-suffix", "prefix-" + proj.ID + "-suffix"},
		{"${route.missing}", ""},              // known namespace, no such sibling
		{"${workload.db.no_such_field}", "${workload.db.no_such_field}"}, // unknown field: untouched
		{"${SOME_OTHER_VAR}", "${SOME_OTHER_VAR}"},                       // unknown namespace: untouched
		{"plain value", "plain value"},
	}
	for _, c := range cases {
		got := m.interpolateEnv(api, map[string]string{"K": c.in})["K"]
		if got != c.want {
			t.Errorf("interpolate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestInterpolatePortAddressingMode: with a PortBase set, ${route.x} resolves
// to the workload's stable localhost port, matching what the admin displays.
func TestInterpolatePortAddressingMode(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{DataRoot: t.TempDir(), BaseDomain: "test.local", PortBase: 18100}, st, reapStub{})
	_, wls, err := m.CreateProject(context.Background(), "demo", "", []WorkloadSpec{
		{Type: runtime.WorkloadTinbaseProject, Workspace: "db"},
		{Type: runtime.WorkloadRapidNativeDev, Name: "api", Workspace: "api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := m.interpolateEnv(wls[1], map[string]string{"K": "${route.db}"})["K"]
	want := m.PublicEndpoint(wls[0])
	if got != want || !strings.HasPrefix(got, "http://localhost:") {
		t.Fatalf("port-mode route = %q, want %q (localhost URL)", got, want)
	}
}

// TestSpecEnvInterpolated proves the boot path end to end: a template project
// whose mobile workload declares ${route.api} / ${workload.db.anon_key} env
// boots with the resolved values in its runtime.Spec — including a BACKWARD
// reference (api declared before db in the manifest referencing db's key),
// which only works because CreateProject registers all workloads before
// booting any.
func TestSpecEnvInterpolated(t *testing.T) {
	tmplDir := t.TempDir()
	manifest := map[string]any{
		"name": "demo",
		"workloads": []map[string]any{
			{"name": "api", "kind": "node", "dir": "api", "run": []string{"node", "index.js"},
				"env": map[string]string{
					"SUPABASE_URL":      "${route.db}", // backward reference
					"SUPABASE_ANON_KEY": "${workload.db.anon_key}",
				}},
			{"name": "db", "kind": "tinbase"},
		},
	}
	mb, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(tmplDir, "orchd.json"), mb, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmplDir, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "api", "index.js"), []byte("// app"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := &recordingRuntime{}
	m := newInterpManager(t, rt)
	if err := m.SetTemplate("demo", tmplDir); err != nil {
		t.Fatal(err)
	}

	proj, wls, err := m.CreateFromTemplate(context.Background(), "demo", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rt.waitSpecs(t, len(wls))

	var apiWl, dbWl *store.Workload
	for _, w := range wls {
		switch w.Workspace {
		case "api":
			apiWl = w
		case "db":
			dbWl = w
		}
	}
	if apiWl == nil || dbWl == nil {
		t.Fatalf("missing workloads: %+v", wls)
	}

	spec, ok := rt.specFor(apiWl.ID)
	if !ok {
		t.Fatalf("api workload never provisioned")
	}
	wantURL := "http://" + proj.ID + ".test.local:8091"
	if spec.Env["SUPABASE_URL"] != wantURL {
		t.Errorf("SUPABASE_URL = %q, want %q", spec.Env["SUPABASE_URL"], wantURL)
	}
	// Minted keys are empty in unit tests (no tinbase binary); the credential
	// token must still resolve to the sibling's value, not leak the token.
	if spec.Env["SUPABASE_ANON_KEY"] != dbWl.AnonKey {
		t.Errorf("SUPABASE_ANON_KEY = %q, want db anon key %q", spec.Env["SUPABASE_ANON_KEY"], dbWl.AnonKey)
	}
	if strings.Contains(spec.Env["SUPABASE_ANON_KEY"], "${") {
		t.Errorf("unresolved token leaked into spec: %q", spec.Env["SUPABASE_ANON_KEY"])
	}
	if strings.Contains(spec.Env["SUPABASE_URL"], "${") {
		t.Errorf("unresolved token leaked into spec: %q", spec.Env["SUPABASE_URL"])
	}
}

// TestPrimaryFlagRouting: a manifest that marks a non-tinbase workload primary
// gives it the bare <ref>.<base> route, the tinbase workload gets a named one,
// and ${route.*} interpolation follows. Legacy manifests (no flag) keep
// tinbase as primary.
func TestPrimaryFlagRouting(t *testing.T) {
	tmplDir := t.TempDir()
	manifest := `{"name":"demo","workloads":[
		{"name":"db","kind":"tinbase"},
		{"name":"mobile","kind":"node","dir":"mobile","run":["node","x.js"],"primary":true}]}`
	if err := os.WriteFile(filepath.Join(tmplDir, "orchd.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmplDir, "mobile"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := newInterpManager(t, reapStub{})
	if err := m.SetTemplate("demo", tmplDir); err != nil {
		t.Fatal(err)
	}
	proj, wls, err := m.CreateFromTemplate(context.Background(), "demo", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	var db, mobile *store.Workload
	for _, w := range wls {
		if w.Workspace == "db" {
			db = w
		} else if w.Workspace == "mobile" {
			mobile = w
		}
	}
	if mobile.Name != "" || db.Name != "db" {
		t.Fatalf("primary flag not applied: mobile.Name=%q db.Name=%q", mobile.Name, db.Name)
	}
	// Routes: mobile owns the bare host, db is suffixed.
	if got := m.PublicEndpoint(mobile); got != "http://"+proj.ID+".test.local:8091" {
		t.Fatalf("mobile endpoint = %q", got)
	}
	if got := m.PublicEndpoint(db); got != "http://"+proj.ID+"-db.test.local:8091" {
		t.Fatalf("db endpoint = %q", got)
	}
	// Interpolation still resolves by workspace name.
	env := m.interpolateEnv(mobile, map[string]string{"K": "${route.db}"})
	if env["K"] != "http://"+proj.ID+"-db.test.local:8091" {
		t.Fatalf("route.db = %q", env["K"])
	}
	// And the route records exist as minted.
	if _, err := m.store.GetRouteByHost(proj.ID + ".test.local"); err != nil {
		t.Fatal("bare host route missing")
	}
	if _, err := m.store.GetRouteByHost(proj.ID + "-db.test.local"); err != nil {
		t.Fatal("db route missing")
	}
}

// TestWorkspaceMountVolumeRun: docker volume-run mounts are computed for
// seeded node/static workspaces (source over the image app dir, deps volume
// for node) and stay empty for tinbase and unseeded workloads.
func TestWorkspaceMountVolumeRun(t *testing.T) {
	tmplDir := t.TempDir()
	manifest := `{"name":"demo","workloads":[
		{"name":"db","kind":"tinbase"},
		{"name":"web","kind":"static","dir":"web"},
		{"name":"mobile","kind":"node","dir":"mobile","run":["node","x.js"],"primary":true}]}`
	if err := os.WriteFile(filepath.Join(tmplDir, "orchd.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"web", "mobile"} {
		if err := os.MkdirAll(filepath.Join(tmplDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	m := newInterpManager(t, reapStub{})
	if err := m.SetTemplate("demo", tmplDir); err != nil {
		t.Fatal(err)
	}
	_, wls, err := m.CreateFromTemplate(context.Background(), "demo", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, w := range wls {
		spec := m.specFor(w)
		switch w.Workspace {
		case "mobile":
			if spec.WorkspaceDir != "mobile" || spec.AppMount != "/app" || spec.DepsPath != "/app/node_modules" {
				t.Fatalf("mobile mounts = %q %q %q", spec.WorkspaceDir, spec.AppMount, spec.DepsPath)
			}
		case "web":
			if spec.WorkspaceDir != "web" || spec.AppMount != "/usr/share/nginx/html" || spec.DepsPath != "" {
				t.Fatalf("web mounts = %q %q %q", spec.WorkspaceDir, spec.AppMount, spec.DepsPath)
			}
		case "db":
			if spec.AppMount != "" {
				t.Fatalf("tinbase must run its platform image untouched, got mount %q", spec.AppMount)
			}
		}
	}
}

// Platform env (ORCHD_WORKLOAD_ENV) is the lowest layer: project env overrides
// it, per-workload env overrides both, and it must never displace the seeded
// JWT secret.
func TestSpecForPlatformEnvPrecedence(t *testing.T) {
	st, _ := store.Open("")
	cfg := config.Config{
		DataRoot: t.TempDir(),
		WorkloadEnv: map[string]string{
			"npm_config_registry": "http://172.17.0.1:4873",
			"FROM_PLATFORM":       "platform",
			"TINBASE_JWT_SECRET":  "must-not-win",
		},
	}
	m := New(cfg, st, nil)
	_ = st.PutProject(&store.Project{ID: "p", Env: map[string]string{"FROM_PLATFORM": "project"}})
	w := &store.Workload{ID: "w", ProjectID: "p", JWTSecret: "real-secret",
		Env: map[string]string{"OWN": "workload"}}
	_ = st.PutWorkload(w)

	env := m.specFor(w).Env
	if env["npm_config_registry"] != "http://172.17.0.1:4873" {
		t.Fatalf("platform key missing: %v", env)
	}
	if env["FROM_PLATFORM"] != "project" {
		t.Fatalf("project env must override platform: %q", env["FROM_PLATFORM"])
	}
	if env["OWN"] != "workload" {
		t.Fatalf("workload env lost: %q", env["OWN"])
	}
	if env["TINBASE_JWT_SECRET"] != "real-secret" {
		t.Fatal("platform env must not displace the seeded JWT secret")
	}
}
