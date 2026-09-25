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
		{"${route.missing}", ""},                                         // known namespace, no such sibling
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

// ORCHD_TINBASE_ENV reaches tinbase workloads only: it carries the Resend key
// for auth email, which must never land in a tenant's api container. tinbase
// also gets its public route as TINBASE_SITE_URL so emailed links are
// reachable, unless the platform or project says otherwise.
func TestSpecForTinbaseOnlyEnv(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{
		DataRoot:    t.TempDir(),
		WorkloadEnv: map[string]string{"FROM_PLATFORM": "platform"},
		TinbaseEnv: map[string]string{
			"TINBASE_RESEND_API_KEY": "re_secret",
			"TINBASE_MAIL_FROM":      "App <noreply@example.com>",
		},
	}, st, nil)
	_ = st.PutProject(&store.Project{ID: "p"})
	db := &store.Workload{ID: "db", ProjectID: "p", Type: runtime.WorkloadTinbaseProject, Workspace: "db", JWTSecret: "s", HostPort: 54321}
	api := &store.Workload{ID: "api", ProjectID: "p", Type: runtime.WorkloadRapidNativeDev, Workspace: "api"}
	_ = st.PutWorkload(db)
	_ = st.PutWorkload(api)

	dbEnv := m.specFor(db).Env
	if dbEnv["TINBASE_RESEND_API_KEY"] != "re_secret" || dbEnv["TINBASE_MAIL_FROM"] != "App <noreply@example.com>" {
		t.Fatalf("tinbase env missing from the db workload: %v", dbEnv)
	}
	if dbEnv["FROM_PLATFORM"] != "platform" {
		t.Fatal("tinbase must still receive the platform-wide env")
	}
	if dbEnv["TINBASE_SITE_URL"] != "http://localhost:54321" {
		t.Fatalf("tinbase should get its public endpoint as TINBASE_SITE_URL, got %q", dbEnv["TINBASE_SITE_URL"])
	}

	apiEnv := m.specFor(api).Env
	if _, leaked := apiEnv["TINBASE_RESEND_API_KEY"]; leaked {
		t.Fatal("the Resend key must not reach a tenant's api container")
	}
	if _, ok := apiEnv["TINBASE_SITE_URL"]; ok {
		t.Fatal("TINBASE_SITE_URL is tinbase-only")
	}

	// A project can still pin its own site URL (custom domain).
	_ = st.PutProject(&store.Project{ID: "p", Env: map[string]string{"TINBASE_SITE_URL": "https://db.custom.example"}})
	if got := m.specFor(db).Env["TINBASE_SITE_URL"]; got != "https://db.custom.example" {
		t.Fatalf("project env must override the derived site URL, got %q", got)
	}
}

// site_url is the app, api_external_url is tinbase. Collapsed into one, a link
// that cannot be honoured sends the user to whichever of the two it was set to:
// the database, where they get a JSON health body instead of a page.
func TestSpecForTinbaseSiteUrlIsTheApp(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{DataRoot: t.TempDir(), BaseDomain: "test.local", PublicScheme: "https"}, st, nil)
	_ = st.PutProject(&store.Project{ID: "p"})
	db := &store.Workload{ID: "db", ProjectID: "p", Name: "db", Type: runtime.WorkloadTinbaseProject, Workspace: "db", JWTSecret: "s"}
	mobile := &store.Workload{ID: "mob", ProjectID: "p", Name: "mobile", Type: runtime.WorkloadRapidNativeDev, Workspace: "mobile"}
	_ = st.PutWorkload(db)
	_ = st.PutWorkload(mobile)

	env := m.specFor(db).Env
	if got, want := env["TINBASE_API_EXTERNAL_URL"], m.PublicEndpoint(db); got != want {
		t.Fatalf("emailed links must be built on tinbase's own endpoint: got %q want %q", got, want)
	}
	if got, want := env["TINBASE_SITE_URL"], m.PublicEndpoint(mobile); got != want {
		t.Fatalf("the default redirect must be the app: got %q want %q", got, want)
	}
	if env["TINBASE_SITE_URL"] == env["TINBASE_API_EXTERNAL_URL"] {
		t.Fatal("the two must differ once the project has an app workload")
	}
}

// With no app workload there is nowhere better to point, so site_url keeps the
// pre-split value rather than being left empty.
func TestSpecForTinbaseSiteUrlFallsBackWithoutAnApp(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{DataRoot: t.TempDir(), BaseDomain: "test.local", PublicScheme: "https"}, st, nil)
	_ = st.PutProject(&store.Project{ID: "p"})
	db := &store.Workload{ID: "db", ProjectID: "p", Type: runtime.WorkloadTinbaseProject, Workspace: "db", JWTSecret: "s"}
	_ = st.PutWorkload(db)

	env := m.specFor(db).Env
	if got, want := env["TINBASE_SITE_URL"], m.PublicEndpoint(db); got != want {
		t.Fatalf("site_url should fall back to tinbase's endpoint, got %q", got)
	}
}

// Expo inlines only EXPO_PUBLIC_-prefixed variables into the client bundle, so
// the unprefixed origin reads as undefined in the app - and code building an
// absolute URL from it sends a password-reset link with no redirect_to at all.
func TestSpecForMirrorsExpoOriginUnderThePublicName(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{DataRoot: t.TempDir(), BaseDomain: "test.local", PublicScheme: "https"}, st, nil)
	_ = st.PutProject(&store.Project{ID: "p"})
	mobile := &store.Workload{
		ID: "mob", ProjectID: "p", Type: runtime.WorkloadRapidNativeDev, Workspace: "mobile",
		Env: map[string]string{"EXPO_DEV_SERVER_ORIGIN": "${route.mobile}"},
	}
	_ = st.PutWorkload(mobile)

	env := m.specFor(mobile).Env
	want := m.PublicEndpoint(mobile)
	if env["EXPO_DEV_SERVER_ORIGIN"] != want {
		t.Fatalf("the token should have resolved, got %q", env["EXPO_DEV_SERVER_ORIGIN"])
	}
	if got := env["EXPO_PUBLIC_DEV_SERVER_ORIGIN"]; got != want {
		t.Fatalf("the app can only read the prefixed name: got %q want %q", got, want)
	}
}

// An explicit value wins: the mirror fills a gap, it does not overrule anyone.
func TestSpecForKeepsAnExplicitPublicExpoOrigin(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{DataRoot: t.TempDir(), BaseDomain: "test.local", PublicScheme: "https"}, st, nil)
	_ = st.PutProject(&store.Project{ID: "p"})
	mobile := &store.Workload{
		ID: "mob", ProjectID: "p", Type: runtime.WorkloadRapidNativeDev, Workspace: "mobile",
		Env: map[string]string{
			"EXPO_DEV_SERVER_ORIGIN":        "${route.mobile}",
			"EXPO_PUBLIC_DEV_SERVER_ORIGIN": "https://app.example.com",
		},
	}
	_ = st.PutWorkload(mobile)

	if got := m.specFor(mobile).Env["EXPO_PUBLIC_DEV_SERVER_ORIGIN"]; got != "https://app.example.com" {
		t.Fatalf("an explicit value must survive, got %q", got)
	}
}

// A tinbase workload must be told every origin its project is served on, or
// tinbase's redirect allowlist (enforced on any non-loopback bind) sends a
// password-reset link back to the database host instead of the app's page.
func TestSpecForTinbaseRedirectAllowList(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{DataRoot: t.TempDir(), BaseDomain: "test.local", PublicScheme: "https"}, st, reapStub{})
	proj, wls, err := m.CreateProject(context.Background(), "demo", "", []WorkloadSpec{
		{Type: runtime.WorkloadTinbaseProject, Workspace: "db"},
		{Type: runtime.WorkloadRapidNativeDev, Name: "web", Workspace: "web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	db, web := wls[0], wls[1]

	got := m.specFor(db).Env["TINBASE_URI_ALLOW_LIST"]
	for _, want := range []string{
		"https://" + proj.ID + ".test.local/**",     // the tinbase primary's own route
		"https://" + proj.ID + "-web.test.local/**", // where the reset page is served
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("allow list %q is missing %q", got, want)
		}
	}

	// An extra hostname added later (a second deployment route) must be covered
	// too — it is a perfectly valid place for the app to be opened from.
	if err := m.AddRoute("custom-web.test.local", web.ID); err != nil {
		t.Fatal(err)
	}
	if got := m.specFor(db).Env["TINBASE_URI_ALLOW_LIST"]; !strings.Contains(got, "https://custom-web.test.local/**") {
		t.Fatalf("added route missing from allow list: %q", got)
	}

	// It is tinbase-only: a tenant workload has no business receiving it.
	if _, ok := m.specFor(web).Env["TINBASE_URI_ALLOW_LIST"]; ok {
		t.Fatal("TINBASE_URI_ALLOW_LIST must not be set on non-tinbase workloads")
	}
}

// A tinbase workload is pointed at the platform's mail relay with the
// project's own service key as the password — derived here rather than pushed
// in with the app workloads' env, which deliberately skips the database.
func TestSpecForTinbaseSMTP(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{
		DataRoot:              t.TempDir(),
		TinbaseSMTPHost:       "smtp.relay.test",
		TinbaseSMTPPort:       587,
		TinbaseSMTPAdminEmail: "noreply@relay.test",
		TinbaseSMTPSenderName: "Platform",
	}, st, nil)
	_ = st.PutProject(&store.Project{ID: "p", Env: map[string]string{"RAPIDNATIVE_GLOBAL_SERVICES_KEY": "rn_svc_abc"}})
	db := &store.Workload{ID: "db", ProjectID: "p", Type: runtime.WorkloadTinbaseProject, Workspace: "db", JWTSecret: "s"}
	api := &store.Workload{ID: "api", ProjectID: "p", Type: runtime.WorkloadRapidNativeDev, Workspace: "api"}
	_ = st.PutWorkload(db)
	_ = st.PutWorkload(api)

	e := m.specFor(db).Env
	if e["TINBASE_SMTP_HOST"] != "smtp.relay.test" || e["TINBASE_SMTP_PORT"] != "587" {
		t.Fatalf("relay not configured: %v", e)
	}
	// the password is the project's own key, so the relay can attribute and cap
	// this project alone
	if e["TINBASE_SMTP_PASS"] != "rn_svc_abc" {
		t.Fatalf("service key not used as the password: %q", e["TINBASE_SMTP_PASS"])
	}
	if e["TINBASE_SMTP_ADMIN_EMAIL"] != "noreply@relay.test" || e["TINBASE_SMTP_SENDER_NAME"] != "Platform" {
		t.Fatalf("sender not configured: %v", e)
	}
	// a workload that runs tenant code must not receive it
	if _, leaked := m.specFor(api).Env["TINBASE_SMTP_PASS"]; leaked {
		t.Fatal("the service key must not reach a tenant's app container via SMTP vars")
	}
}

// Without a project key there is nothing to authenticate with, so nothing is
// injected — rather than half a configuration that fails on the first send.
func TestSpecForTinbaseSMTPSkippedWithoutKey(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{DataRoot: t.TempDir(), TinbaseSMTPHost: "smtp.relay.test"}, st, nil)
	_ = st.PutProject(&store.Project{ID: "p"})
	db := &store.Workload{ID: "db", ProjectID: "p", Type: runtime.WorkloadTinbaseProject, Workspace: "db"}
	_ = st.PutWorkload(db)
	if _, ok := m.specFor(db).Env["TINBASE_SMTP_HOST"]; ok {
		t.Fatal("SMTP configured with no key to authenticate with")
	}
}

// ORCHD_HOST_ALIASES must reach every instance spec — it's how a well-known
// hostname (a CDN, a registry) is steered to an on-box cache for guests only.
func TestSpecForCarriesHostAliases(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{
		DataRoot:    t.TempDir(),
		HostAliases: map[string]string{"esm.example.test": "10.201.255.1"},
	}, st, nil)
	_ = st.PutProject(&store.Project{ID: "p"})
	w := &store.Workload{ID: "w", ProjectID: "p"}
	_ = st.PutWorkload(w)

	got := m.specFor(w).HostAliases
	if got["esm.example.test"] != "10.201.255.1" {
		t.Fatalf("aliases missing from spec: %v", got)
	}
}

// A preset-image workspace (manifest image, no setup/install/build) mounts its
// seeded tree at /app with NOTHING over node_modules — the default node arm
// would shadow both the image's global tools and any node_modules the project
// carries with an empty volume. It also boots the manifest image, publishes
// the manifest port, gets the short ready window, and carries its own limits.
func TestPresetImageWorkspaceSpec(t *testing.T) {
	tmplDir := t.TempDir()
	manifest := `{"name":"demo","workloads":[
		{"name":"db","kind":"tinbase"},
		{"name":"mobile","kind":"node","dir":"mobile","image":"rn-run:dev",
		 "run":["rnrun","start","--port","$PORT"],"port":8080,"memory_mb":1024,"cpus":2,"primary":true}]}`
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
	_, wls, err := m.CreateFromTemplate(context.Background(), "demo", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wls {
		if w.Workspace != "mobile" {
			continue
		}
		spec := m.specFor(w)
		if spec.Image != "rn-run:dev" {
			t.Fatalf("preset must boot the manifest image, got %q", spec.Image)
		}
		if spec.AppMount != "/app" || spec.DepsPath != "" || spec.DepsHostDir != "" {
			t.Fatalf("preset mounts = app:%q deps:%q host:%q (deps must be empty)", spec.AppMount, spec.DepsPath, spec.DepsHostDir)
		}
		if spec.Port != 8080 {
			t.Fatalf("manifest port must flow to the spec, got %d", spec.Port)
		}
		if spec.ReadyTimeout != 120*time.Second {
			t.Fatalf("preset ready window = %v, want 120s", spec.ReadyTimeout)
		}
		if spec.Limits.MemoryMB != 1024 {
			t.Fatalf("manifest memory_mb must flow to limits, got %d", spec.Limits.MemoryMB)
		}
		return
	}
	t.Fatal("mobile workload not created")
}

// Baked workspaces keep the long ready window: their first boot can run a real
// install, and tearing that down at 120s put boot loops into production once.
func TestBakedWorkspaceKeepsLongReadyWindow(t *testing.T) {
	tmplDir := t.TempDir()
	manifest := `{"name":"demo","workloads":[
		{"name":"api","kind":"node","dir":"api","install":["bun","install"],"run":["node","x.js"],"primary":true}]}`
	if err := os.WriteFile(filepath.Join(tmplDir, "orchd.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmplDir, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := newInterpManager(t, reapStub{})
	if err := m.SetTemplate("demo", tmplDir); err != nil {
		t.Fatal(err)
	}
	_, wls, err := m.CreateFromTemplate(context.Background(), "demo", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.specFor(wls[0]).ReadyTimeout; got != 15*time.Minute {
		t.Fatalf("baked workspace ready window = %v, want 15m", got)
	}
}
