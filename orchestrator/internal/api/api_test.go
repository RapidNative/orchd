package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/api"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/manager"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// stubRuntime is a hermetic runtime.Runtime that launches nothing, so the API
// layer can be exercised without Docker or a tinbase binary. It deliberately
// does NOT implement runtime.ImageManager, which lets us assert the capability
// gate (image endpoints return 501).
type stubRuntime struct{}

func (stubRuntime) Name() string { return "stub" }
func (stubRuntime) Stats(context.Context, string) (runtime.Stats, error) {
	return runtime.Stats{}, nil
}
func (stubRuntime) Logs(context.Context, string, int) (string, error) { return "", nil }
func (stubRuntime) Create(_ context.Context, s runtime.Spec) (*runtime.Instance, error) {
	return &runtime.Instance{Ref: s.Ref, State: runtime.StateRunning, Addr: "127.0.0.1:1"}, nil
}
func (stubRuntime) Start(_ context.Context, s runtime.Spec) (*runtime.Instance, error) {
	return &runtime.Instance{Ref: s.Ref, State: runtime.StateRunning, Addr: "127.0.0.1:1"}, nil
}
func (stubRuntime) Suspend(context.Context, string) error { return nil }
func (stubRuntime) Stop(context.Context, string) error    { return nil }
func (stubRuntime) Status(context.Context, string) (runtime.State, error) {
	return runtime.StateRunning, nil
}

const bootstrapKey = "test-admin-key"

// newTestServer wires the real router, auth middleware, manager, and an
// in-memory store against the stub runtime.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := config.Load()
	cfg.DataRoot = t.TempDir()
	cfg.BaseDomain = "test.local"

	st, err := store.Open("") // empty path => in-memory
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	mgr := manager.New(cfg, st, stubRuntime{})
	srv := httptest.NewServer(api.New(mgr, cfg, bootstrapKey).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// do issues a request with an optional bearer key and returns status + body.
func do(t *testing.T, srv *httptest.Server, method, path, key string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, srv.URL+path, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out
}

func TestHealthzIsOpen(t *testing.T) {
	srv := newTestServer(t)
	if code, _ := do(t, srv, "GET", "/healthz", "", nil); code != http.StatusOK {
		t.Fatalf("healthz without key: got %d, want 200", code)
	}
}

func TestAuth(t *testing.T) {
	srv := newTestServer(t)
	cases := []struct {
		name, key string
		want      int
	}{
		{"no key", "", http.StatusUnauthorized},
		{"wrong key", "nope", http.StatusUnauthorized},
		{"bootstrap key", bootstrapKey, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if code, _ := do(t, srv, "GET", "/v1/projects", c.key, nil); code != c.want {
				t.Fatalf("got %d, want %d", code, c.want)
			}
		})
	}
}

func TestProjectLifecycle(t *testing.T) {
	srv := newTestServer(t)

	// Empty list to start.
	code, body := do(t, srv, "GET", "/v1/projects", bootstrapKey, nil)
	if code != http.StatusOK || string(bytes.TrimSpace(body)) != "[]" {
		t.Fatalf("initial list: %d %s", code, body)
	}

	// Create a project (default body => one tinbase workload).
	code, body = do(t, srv, "POST", "/v1/projects", bootstrapKey, map[string]any{})
	if code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201 (%s)", code, body)
	}
	var proj struct {
		ID        string `json:"id"`
		Workloads []struct {
			ID string `json:"id"`
		} `json:"workloads"`
	}
	if err := json.Unmarshal(body, &proj); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	if proj.ID == "" || len(proj.Workloads) != 1 {
		t.Fatalf("unexpected project: %s", body)
	}

	// Fetch it back.
	if code, _ := do(t, srv, "GET", "/v1/projects/"+proj.ID, bootstrapKey, nil); code != http.StatusOK {
		t.Fatalf("get project: got %d, want 200", code)
	}

	// Delete it.
	if code, _ := do(t, srv, "DELETE", "/v1/projects/"+proj.ID, bootstrapKey, nil); code != http.StatusNoContent {
		t.Fatalf("delete project: got %d, want 204", code)
	}
	if code, _ := do(t, srv, "GET", "/v1/projects/"+proj.ID, bootstrapKey, nil); code != http.StatusNotFound {
		t.Fatalf("get deleted project: got %d, want 404", code)
	}
}

func TestReadonlyRoleIsBlockedOnWrites(t *testing.T) {
	srv := newTestServer(t)

	// Mint a readonly key (admin action).
	code, body := do(t, srv, "POST", "/v1/keys", bootstrapKey,
		map[string]any{"name": "ci", "role": "readonly"})
	if code != http.StatusCreated {
		t.Fatalf("create key: got %d (%s)", code, body)
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Key == "" {
		t.Fatalf("expected plaintext key once, got: %s", body)
	}

	// Readonly may GET.
	if code, _ := do(t, srv, "GET", "/v1/projects", created.Key, nil); code != http.StatusOK {
		t.Fatalf("readonly GET: got %d, want 200", code)
	}
	// Readonly may NOT create a project.
	if code, _ := do(t, srv, "POST", "/v1/projects", created.Key, map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("readonly POST: got %d, want 403", code)
	}
}

func TestImagesUnsupportedReturns501(t *testing.T) {
	srv := newTestServer(t)
	// The stub runtime is not an ImageManager, so image management is a 501.
	if code, _ := do(t, srv, "GET", "/v1/images", bootstrapKey, nil); code != http.StatusNotImplemented {
		t.Fatalf("images list on non-image runtime: got %d, want 501", code)
	}
}

func TestInstanceName(t *testing.T) {
	srv := newTestServer(t)

	// Unset by default.
	_, body := do(t, srv, "GET", "/v1/settings", bootstrapKey, nil)
	if got := gjson(body, "instance_name"); got != "" {
		t.Fatalf("default instance_name = %q, want empty", got)
	}

	// Set it (admin), then both settings and info reflect it.
	if code, _ := do(t, srv, "PUT", "/v1/settings/name", bootstrapKey,
		map[string]any{"instance_name": "tinbase cloud"}); code != http.StatusOK {
		t.Fatalf("set name: got %d", code)
	}
	_, body = do(t, srv, "GET", "/v1/settings", bootstrapKey, nil)
	if got := gjson(body, "instance_name"); got != "tinbase cloud" {
		t.Fatalf("settings instance_name = %q", got)
	}
	_, body = do(t, srv, "GET", "/v1/info", bootstrapKey, nil)
	if got := gjson(body, "instance_name"); got != "tinbase cloud" {
		t.Fatalf("info instance_name = %q", got)
	}

	// Readonly key may not change it.
	code, kb := do(t, srv, "POST", "/v1/keys", bootstrapKey,
		map[string]any{"name": "ro", "role": "readonly"})
	if code != http.StatusCreated {
		t.Fatalf("mint key: %d", code)
	}
	var k struct {
		Key string `json:"key"`
	}
	json.Unmarshal(kb, &k)
	if code, _ := do(t, srv, "PUT", "/v1/settings/name", k.Key,
		map[string]any{"instance_name": "hacked"}); code != http.StatusForbidden {
		t.Fatalf("readonly rename: got %d, want 403", code)
	}
}

// gjson pulls a single top-level string field out of a JSON object body.
func gjson(body []byte, key string) string {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func TestPortAddressing(t *testing.T) {
	t.Setenv("ORCHD_PORT_BASE", "8100")
	srv := newTestServer(t)

	// A project with a tinbase backend + two dev apps.
	code, body := do(t, srv, "POST", "/v1/projects", bootstrapKey, map[string]any{
		"workloads": []map[string]any{
			{"preset": "tinbase"},
			{"preset": "vite"},
			{"preset": "api"},
		},
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var proj struct {
		Workloads []struct {
			HostPort  int      `json:"host_port"`
			Endpoints []string `json:"endpoints"`
		} `json:"workloads"`
	}
	if err := json.Unmarshal(body, &proj); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(proj.Workloads) != 3 {
		t.Fatalf("want 3 workloads, got %d", len(proj.Workloads))
	}
	// Each gets a distinct stable port >= base and a direct-port endpoint.
	seen := map[int]bool{}
	for i, w := range proj.Workloads {
		if w.HostPort < 8100 {
			t.Fatalf("workload %d host_port %d < base 8100", i, w.HostPort)
		}
		if seen[w.HostPort] {
			t.Fatalf("duplicate host_port %d", w.HostPort)
		}
		seen[w.HostPort] = true
		want := "http://localhost:" + strconv.Itoa(w.HostPort)
		if len(w.Endpoints) == 0 || w.Endpoints[0] != want {
			t.Fatalf("workload %d endpoints=%v, want first %s", i, w.Endpoints, want)
		}
	}
}

func TestWorkloadEnvInjection(t *testing.T) {
	srv := newTestServer(t)
	code, body := do(t, srv, "POST", "/v1/projects", bootstrapKey, map[string]any{})
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	var proj struct {
		Workloads []struct {
			ID string `json:"id"`
		} `json:"workloads"`
	}
	json.Unmarshal(body, &proj)
	wid := proj.Workloads[0].ID

	if code, _ := do(t, srv, "PUT", "/v1/workloads/"+wid+"/env", bootstrapKey,
		map[string]any{"env": map[string]string{"FOO": "bar"}}); code != http.StatusOK {
		t.Fatalf("set env: %d", code)
	}
	_, wb := do(t, srv, "GET", "/v1/workloads/"+wid, bootstrapKey, nil)
	var wl struct {
		Env map[string]string `json:"env"`
	}
	json.Unmarshal(wb, &wl)
	if wl.Env["FOO"] != "bar" {
		t.Fatalf("env not applied: %v", wl.Env)
	}
}

func TestTLSAllowAcrossDomains(t *testing.T) {
	t.Setenv("ORCHD_ALT_DOMAINS", "tinbase.dev")
	srv := newTestServer(t) // base domain defaults to lvh.me

	cases := []struct {
		domain string
		want   int
	}{
		{"admin.test.local", http.StatusOK},  // base (newTestServer sets test.local)
		{"api.test.local", http.StatusOK},    // base
		{"admin.tinbase.dev", http.StatusOK}, // alt
		{"api.tinbase.dev", http.StatusOK},   // alt
		{"evil.example.com", http.StatusForbidden},
		{"random.test.local", http.StatusForbidden}, // not admin/api, not in route table
	}
	for _, c := range cases {
		t.Run(c.domain, func(t *testing.T) {
			// tls-allow is internal/open — no key.
			code, _ := do(t, srv, "GET", "/internal/tls-allow?domain="+c.domain, "", nil)
			if code != c.want {
				t.Fatalf("tls-allow %s: got %d, want %d", c.domain, code, c.want)
			}
		})
	}
}

func TestDefaultRegionSeeded(t *testing.T) {
	srv := newTestServer(t)
	code, body := do(t, srv, "GET", "/v1/regions", bootstrapKey, nil)
	if code != http.StatusOK {
		t.Fatalf("regions: got %d, want 200", code)
	}
	var regions []struct {
		ID        string `json:"id"`
		IsDefault bool   `json:"is_default"`
	}
	if err := json.Unmarshal(body, &regions); err != nil {
		t.Fatalf("decode regions: %v", err)
	}
	var hasDefault bool
	for _, r := range regions {
		if r.IsDefault {
			hasDefault = true
		}
	}
	if !hasDefault {
		t.Fatalf("expected a seeded default region, got: %s", body)
	}
}

// TestCreateProjectWithBinaryDelta registers a temp template, creates a
// project overlaying both a text delta and a base64 binary delta, and reads
// the binary back byte-identical through the workload fs API.
func TestCreateProjectWithBinaryDelta(t *testing.T) {
	srv := newTestServer(t)

	tmplDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmplDir, "orchd.json"),
		[]byte(`{"name":"demo","workloads":[{"name":"db","kind":"tinbase"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := do(t, srv, "PUT", "/v1/templates", bootstrapKey,
		map[string]string{"name": "demo", "path": tmplDir}); code != http.StatusOK {
		t.Fatalf("set template: %d", code)
	}

	binary := []byte{0xFF, 0xFE, 0x00, 0x89, 'P', 'N', 'G', 0x0D, 0x0A}
	code, body := do(t, srv, "POST", "/v1/projects", bootstrapKey, map[string]any{
		"template":  "demo",
		"delta":     map[string]string{"note.txt": "hello"},
		"delta_b64": map[string]string{"assets/img.png": base64.StdEncoding.EncodeToString(binary)},
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var proj struct {
		Workloads []struct {
			ID string `json:"id"`
		} `json:"workloads"`
	}
	json.Unmarshal(body, &proj)
	wid := proj.Workloads[0].ID

	if code, got := do(t, srv, "GET", "/v1/workloads/"+wid+"/fs/file?path=assets/img.png", bootstrapKey, nil); code != http.StatusOK || !bytes.Equal(got, binary) {
		t.Fatalf("binary delta corrupted: code=%d got=%v want=%v", code, got, binary)
	}
	if code, got := do(t, srv, "GET", "/v1/workloads/"+wid+"/fs/file?path=note.txt", bootstrapKey, nil); code != http.StatusOK || string(got) != "hello" {
		t.Fatalf("text delta: code=%d got=%q", code, got)
	}
}

// TestCreateProjectRejectsBadBase64 ensures a malformed delta_b64 entry fails
// the whole create with a 400 rather than silently writing garbage.
func TestCreateProjectRejectsBadBase64(t *testing.T) {
	srv := newTestServer(t)
	code, _ := do(t, srv, "POST", "/v1/projects", bootstrapKey, map[string]any{
		"delta_b64": map[string]string{"x.bin": "not-base64!!!"},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("bad base64: got %d, want 400", code)
	}
}

// TestWorkloadFileBatch exercises the batch fs endpoint: text write, base64
// binary write, and delete in one call, verified through the fs read API.
func TestWorkloadFileBatch(t *testing.T) {
	srv := newTestServer(t)

	tmplDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmplDir, "orchd.json"),
		[]byte(`{"name":"demo","workloads":[{"name":"db","kind":"tinbase"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "stale.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := do(t, srv, "PUT", "/v1/templates", bootstrapKey,
		map[string]string{"name": "demo", "path": tmplDir}); code != http.StatusOK {
		t.Fatalf("set template: %d", code)
	}
	code, body := do(t, srv, "POST", "/v1/projects", bootstrapKey, map[string]any{"template": "demo"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var proj struct {
		Workloads []struct {
			ID string `json:"id"`
		} `json:"workloads"`
	}
	json.Unmarshal(body, &proj)
	wid := proj.Workloads[0].ID

	binary := []byte{0x00, 0xFF, 0x42}
	code, body = do(t, srv, "PUT", "/v1/workloads/"+wid+"/fs/batch", bootstrapKey, map[string]any{
		"files": []map[string]any{
			{"path": "app/index.tsx", "content": "export default 1"},
			{"path": "assets/b.bin", "content_b64": base64.StdEncoding.EncodeToString(binary)},
			{"path": "stale.txt", "delete": true},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("batch: %d %s", code, body)
	}
	var res struct{ Written, Deleted int }
	json.Unmarshal(body, &res)
	if res.Written != 2 || res.Deleted != 1 {
		t.Fatalf("counts = %+v, want written 2 deleted 1", res)
	}

	if code, got := do(t, srv, "GET", "/v1/workloads/"+wid+"/fs/file?path=app/index.tsx", bootstrapKey, nil); code != http.StatusOK || string(got) != "export default 1" {
		t.Fatalf("text write: %d %q", code, got)
	}
	if code, got := do(t, srv, "GET", "/v1/workloads/"+wid+"/fs/file?path=assets/b.bin", bootstrapKey, nil); code != http.StatusOK || !bytes.Equal(got, binary) {
		t.Fatalf("binary write: %d %v", code, got)
	}
	if code, _ := do(t, srv, "GET", "/v1/workloads/"+wid+"/fs/file?path=stale.txt", bootstrapKey, nil); code != http.StatusNotFound {
		t.Fatalf("delete: file still readable (%d)", code)
	}

	// Missing path fails the whole batch up front.
	if code, _ := do(t, srv, "PUT", "/v1/workloads/"+wid+"/fs/batch", bootstrapKey, map[string]any{
		"files": []map[string]any{{"content": "x"}},
	}); code != http.StatusBadRequest {
		t.Fatalf("pathless entry: got %d, want 400", code)
	}
}

// TestProjectResetRoute drives reset end-to-end over HTTP: dirty a workload
// through the fs API, POST /v1/projects/{id}/reset with a fresh delta, and
// verify the tree is pristine + delta with the same route hosts.
func TestProjectResetRoute(t *testing.T) {
	srv := newTestServer(t)

	tmplDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmplDir, "orchd.json"),
		[]byte(`{"name":"demo","workloads":[{"name":"db","kind":"tinbase"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "app.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := do(t, srv, "PUT", "/v1/templates", bootstrapKey,
		map[string]string{"name": "demo", "path": tmplDir}); code != http.StatusOK {
		t.Fatalf("set template: %d", code)
	}
	code, body := do(t, srv, "POST", "/v1/projects", bootstrapKey, map[string]any{"template": "demo"})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var proj struct {
		ID        string `json:"id"`
		Workloads []struct {
			ID     string   `json:"id"`
			Routes []string `json:"routes"`
		} `json:"workloads"`
	}
	json.Unmarshal(body, &proj)
	wid := proj.Workloads[0].ID
	routesBefore := proj.Workloads[0].Routes

	// Dirty the tree over the fs API.
	req, _ := http.NewRequest("PUT", srv.URL+"/v1/workloads/"+wid+"/fs/file?path=app.txt", bytes.NewReader([]byte("dirty")))
	req.Header.Set("Authorization", "Bearer "+bootstrapKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("dirty write: %v %d", err, res.StatusCode)
	}
	res.Body.Close()

	code, body = do(t, srv, "POST", "/v1/projects/"+proj.ID+"/reset", bootstrapKey,
		map[string]any{"delta": map[string]string{"fresh.txt": "overlay"}})
	if code != http.StatusOK {
		t.Fatalf("reset: %d %s", code, body)
	}

	if _, got := do(t, srv, "GET", "/v1/workloads/"+wid+"/fs/file?path=app.txt", bootstrapKey, nil); string(got) != "base" {
		t.Fatalf("base not restored: %q", got)
	}
	if _, got := do(t, srv, "GET", "/v1/workloads/"+wid+"/fs/file?path=fresh.txt", bootstrapKey, nil); string(got) != "overlay" {
		t.Fatalf("reset delta not applied: %q", got)
	}
	_, body = do(t, srv, "GET", "/v1/projects/"+proj.ID, bootstrapKey, nil)
	json.Unmarshal(body, &proj)
	if len(proj.Workloads[0].Routes) != len(routesBefore) || proj.Workloads[0].Routes[0] != routesBefore[0] {
		t.Fatalf("routes changed across reset: %v -> %v", routesBefore, proj.Workloads[0].Routes)
	}
}

// A project with no workloads must serialize an empty array, never null: the
// admin panel maps over the field in a dozen places, and a null took down every
// page that listed projects.
func TestProjectWithNoWorkloadsSerializesEmptyArray(t *testing.T) {
	srv := newTestServer(t)

	code, body := do(t, srv, "POST", "/v1/projects", bootstrapKey, map[string]any{})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}
	var created struct {
		ID        string `json:"id"`
		Workloads []struct {
			ID string `json:"id"`
		} `json:"workloads"`
	}
	_ = json.Unmarshal(body, &created)

	// Delete every workload, leaving the project itself — the state a project
	// reaches mid-create, and after its workloads are removed.
	for _, w := range created.Workloads {
		if code, out := do(t, srv, "DELETE", "/v1/workloads/"+w.ID, bootstrapKey, nil); code >= 300 {
			t.Fatalf("delete workload: %d %s", code, out)
		}
	}

	for _, path := range []string{"/v1/projects/" + created.ID, "/v1/projects"} {
		_, raw := do(t, srv, "GET", path, bootstrapKey, nil)
		if bytes.Contains(raw, []byte(`"workloads":null`)) {
			t.Fatalf("%s serialized workloads as null: %s", path, raw)
		}
		if !bytes.Contains(raw, []byte(`"workloads":[]`)) {
			t.Fatalf("%s did not contain an empty workloads array: %s", path, raw)
		}
	}
}
