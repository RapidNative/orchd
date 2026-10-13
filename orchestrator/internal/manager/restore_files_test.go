package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/backup"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// A restore with RefreshFiles keeps the backup's data but takes code and config
// from the workload's template plus the delta, in one step.
func TestRestoreWorkloadWithRefreshesFilesOverTheData(t *testing.T) {
	tmplDir := t.TempDir()
	write := func(dir, rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	write(tmplDir, "orchd.json", `{"name":"demo","workloads":[{"name":"db","kind":"tinbase"}]}`)
	write(tmplDir, "seed.sql", "select 1;")

	st, _ := store.Open("")
	m := New(config.Config{DataRoot: t.TempDir(), BaseDomain: "test.local"}, st, reapStub{})
	bk, err := backup.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.backups = bk
	if err := m.SetTemplate("demo", tmplDir); err != nil {
		t.Fatal(err)
	}
	_, wls, err := m.CreateFromTemplate(context.Background(), "demo", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := wls[0]

	// The backup: data plus stale copies of files the caller has since changed.
	write(w.DataDir, "db/data.bin", "rows")
	write(w.DataDir, "seed.sql", "stale seed")
	write(w.DataDir, "gone.txt", "removed since")
	b, err := bk.Create(w.ID, w.DataDir, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Plain restore: the backup's files win.
	if err := m.RestoreWorkload(context.Background(), w.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if got := read(filepath.Join(w.DataDir, "seed.sql")); got != "stale seed" {
		t.Fatalf("plain restore seed.sql = %q, want the backup's", got)
	}

	opts := RestoreOptions{
		RefreshFiles: true,
		Delta:        map[string][]byte{"supabase/migrations/2_orders.sql": []byte("create table orders();")},
		Deleted:      []string{"gone.txt"},
	}
	if err := m.RestoreWorkloadWith(context.Background(), w.ID, b.ID, opts); err != nil {
		t.Fatal(err)
	}
	if got := read(filepath.Join(w.DataDir, "db/data.bin")); got != "rows" {
		t.Fatalf("data not restored: %q", got)
	}
	if got := read(filepath.Join(w.DataDir, "seed.sql")); got != "select 1;" {
		t.Fatalf("seed.sql = %q, want the template's", got)
	}
	if got := read(filepath.Join(w.DataDir, "supabase/migrations/2_orders.sql")); got != "create table orders();" {
		t.Fatalf("delta not applied: %q", got)
	}
	if _, err := os.Stat(filepath.Join(w.DataDir, "gone.txt")); !os.IsNotExist(err) {
		t.Fatal("a deleted file survived the restore")
	}
}
