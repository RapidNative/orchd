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
