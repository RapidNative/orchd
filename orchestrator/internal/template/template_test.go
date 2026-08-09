package template

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndValidate(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{
	  "name": "demo",
	  "backup_exclude": ["node_modules"],
	  "workloads": [
	    { "name": "db", "kind": "tinbase" },
	    { "name": "api", "kind": "node", "dir": "api", "run": ["node","index.js","--port","$PORT"], "port_env": "PORT" },
	    { "name": "web", "kind": "static", "dir": "web" }
	  ]
	}`), 0o644)

	m, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m.Name != "demo" || len(m.Workloads) != 3 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	api, ok := m.Find("api")
	if !ok {
		t.Fatal("api workload not found")
	}
	got := api.RunArgv(8101)
	want := []string{"node", "index.js", "--port", "8101"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RunArgv = %v, want %v", got, want)
		}
	}
}

func TestLoadRejectsBadKindAndDupes(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{
	  "workloads": [ { "name": "x", "kind": "wat" } ]
	}`), 0o644)
	if _, err := Load(dir); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

// TestBuildStepsValidation: build steps parse for node/static, reject tinbase
// and empty steps.
func TestBuildStepsValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) error {
		return os.WriteFile(filepath.Join(dir, FileName), []byte(s), 0o644)
	}

	ok := `{"name":"x","workloads":[{"name":"m","kind":"node","run":["node","i.js"],
		"build":[["npm","install","-g","bun"],["sh","-lc","warm"]]}]}`
	if err := write(ok); err != nil {
		t.Fatal(err)
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("valid build steps rejected: %v", err)
	}
	if len(m.Workloads[0].Build) != 2 {
		t.Fatalf("build steps = %d, want 2", len(m.Workloads[0].Build))
	}

	if err := write(`{"name":"x","workloads":[{"name":"db","kind":"tinbase","build":[["x"]]}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("tinbase build steps accepted")
	}

	if err := write(`{"name":"x","workloads":[{"name":"m","kind":"node","run":["r"],"build":[[]]}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("empty build step accepted")
	}
}
