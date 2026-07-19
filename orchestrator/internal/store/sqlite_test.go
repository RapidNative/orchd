package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

func TestSQLiteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orchd.db")

	st, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.PutProject(&store.Project{ID: "p1", Name: "acme", Region: "local"}); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Reopen: data must survive (durable) via the SQLite file.
	st2, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	p, err := st2.GetProject("p1")
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if p.Name != "acme" {
		t.Fatalf("got name %q, want acme", p.Name)
	}
}

func TestSQLiteMigratesLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	// A legacy JSON snapshot sitting where the file store wrote it.
	legacy := `{"projects":[{"id":"old1","name":"legacy","region":"local"}]}`
	if err := os.WriteFile(filepath.Join(dir, "projects.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := store.OpenSQLite(filepath.Join(dir, "orchd.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	p, err := st.GetProject("old1")
	if err != nil {
		t.Fatalf("legacy project not migrated: %v", err)
	}
	if p.Name != "legacy" {
		t.Fatalf("got %q, want legacy", p.Name)
	}
}

func TestSQLiteCheckpoint(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "orchd.db"))
	if err != nil {
		t.Fatal(err)
	}
	// The store exposes Checkpoint (used before an off-box state backup).
	cp, ok := st.(interface{ Checkpoint() error })
	if !ok {
		t.Fatal("sqlite store should expose Checkpoint()")
	}
	if err := cp.Checkpoint(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
}
