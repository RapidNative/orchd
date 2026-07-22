package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
