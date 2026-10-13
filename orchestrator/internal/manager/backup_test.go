package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// newBackupMgr is a manager with a local backup store, one database workload
// ("db") and one runner ("app"), each with a file in its data dir.
func newBackupMgr(t *testing.T, cfg config.Config) *Manager {
	t.Helper()
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cfg.DataRoot = root
	cfg.BackupDir = filepath.Join(root, "backups")
	if cfg.BackupRetain == 0 {
		cfg.BackupRetain = 7
	}
	m := New(cfg, st, reapStub{})
	_ = st.PutProject(&store.Project{ID: "p"})
	for id, typ := range map[string]runtime.WorkloadType{"db": runtime.WorkloadTinbaseProject, "app": runtime.WorkloadRapidNativeDev} {
		dir := filepath.Join(root, "projects", "p", id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "data"), []byte(id), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = st.PutWorkload(&store.Workload{ID: id, ProjectID: "p", Type: typ, State: runtime.StateSuspended, DataDir: dir})
	}
	return m
}

func backupCount(t *testing.T, m *Manager, id string) int {
	t.Helper()
	list, err := m.ListBackups(id)
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

// backdate makes every backup of a workload look an hour old: archive names
// have second precision, so a test that writes right after a backup would
// otherwise land in the same second.
func backdate(t *testing.T, m *Manager, id string) {
	t.Helper()
	dir := filepath.Join(m.cfg.BackupDir, id)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		old := time.Now().UTC().Add(-time.Hour).Format("20060102T150405Z") + ".tar.gz"
		if err := os.Rename(filepath.Join(dir, e.Name()), filepath.Join(dir, old)); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	_ = filepath.Walk(filepath.Join(m.cfg.DataRoot, "projects", "p", id), func(p string, _ os.FileInfo, _ error) error {
		return os.Chtimes(p, past, past)
	})
}

func TestScheduledBackupOnlyCoversConfiguredTypes(t *testing.T) {
	m := newBackupMgr(t, config.Config{BackupTypes: []string{"tinbase-project"}})
	m.backupAll(context.Background())
	if got := backupCount(t, m, "db"); got != 1 {
		t.Fatalf("db backups = %d, want 1", got)
	}
	if got := backupCount(t, m, "app"); got != 0 {
		t.Fatalf("runner backups = %d, want 0: its files come from elsewhere", got)
	}
}

func TestScheduledBackupCoversEveryTypeByDefault(t *testing.T) {
	m := newBackupMgr(t, config.Config{})
	m.backupAll(context.Background())
	if backupCount(t, m, "db") != 1 || backupCount(t, m, "app") != 1 {
		t.Fatal("with no BackupTypes every workload is backed up, as before")
	}
}

func TestScheduledBackupSkipsUnchangedData(t *testing.T) {
	m := newBackupMgr(t, config.Config{BackupTypes: []string{"tinbase-project"}})
	m.backupAll(context.Background())
	backdate(t, m, "db")

	m.backupAll(context.Background())
	if got := backupCount(t, m, "db"); got != 1 {
		t.Fatalf("db backups = %d after an idle interval, want 1", got)
	}

	// A write after the newest backup makes the next one run.
	if err := os.WriteFile(filepath.Join(m.cfg.DataRoot, "projects", "p", "db", "data"), []byte("new row"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.backupAll(context.Background())
	if got := backupCount(t, m, "db"); got != 2 {
		t.Fatalf("db backups = %d after a write, want 2", got)
	}
}

func TestUnchangedIgnoresExcludedDirs(t *testing.T) {
	m := newBackupMgr(t, config.Config{})
	m.backupAll(context.Background())
	backdate(t, m, "app")
	deps := filepath.Join(m.cfg.DataRoot, "projects", "p", "app", "node_modules")
	if err := os.MkdirAll(deps, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deps, "x.js"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The new node_modules dir changes the parent's mtime, which a backup does
	// copy; settle it so only the excluded dir is newer.
	past := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(filepath.Dir(deps), past, past)
	w, _ := m.store.GetWorkload("app")
	if !unchangedSinceBackup(m.backupStore(), w) {
		t.Fatal("a write inside node_modules, which backups leave out, counted as a change")
	}
}

func TestDeletedWorkloadKeepsOnlyItsNewestBackup(t *testing.T) {
	m := newBackupMgr(t, config.Config{BackupRetainDeleted: 1})
	for i := 0; i < 3; i++ {
		if _, err := m.BackupWorkload(context.Background(), "db"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1100 * time.Millisecond) // archive names have second precision
	}
	if got := backupCount(t, m, "db"); got != 3 {
		t.Fatalf("setup: %d backups, want 3", got)
	}
	if err := m.DeleteWorkload(context.Background(), "db"); err != nil {
		t.Fatal(err)
	}
	if got := backupCount(t, m, "db"); got != 1 {
		t.Fatalf("deleted workload keeps %d backups, want its newest only", got)
	}
}

func TestDeletedWorkloadOfAnUnbackedTypeKeepsNone(t *testing.T) {
	m := newBackupMgr(t, config.Config{BackupTypes: []string{"tinbase-project"}, BackupRetainDeleted: 1})
	if _, err := m.BackupWorkload(context.Background(), "app"); err != nil { // an old backup from before the filter
		t.Fatal(err)
	}
	if err := m.DeleteProject(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	if got := backupCount(t, m, "app"); got != 0 {
		t.Fatalf("runner keeps %d backups after deletion, want 0", got)
	}
}

func TestDeletedBackupsLeftAloneByDefault(t *testing.T) {
	m := newBackupMgr(t, config.Config{BackupRetainDeleted: -1})
	if _, err := m.BackupWorkload(context.Background(), "db"); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteWorkload(context.Background(), "db"); err != nil {
		t.Fatal(err)
	}
	if got := backupCount(t, m, "db"); got != 1 {
		t.Fatalf("with the default, a deleted workload's backups stay (%d left)", got)
	}
}

func TestResetBacksUpFirst(t *testing.T) {
	m := newBackupMgr(t, config.Config{BackupTypes: []string{"tinbase-project"}, BackupBeforeReset: true})
	if err := m.ResetWorkload(context.Background(), "db", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := backupCount(t, m, "db"); got != 1 {
		t.Fatalf("db backups after a reset = %d, want 1 taken before the wipe", got)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.DataRoot, "projects", "p", "db", "data")); !os.IsNotExist(err) {
		t.Fatal("the reset should still wipe the data dir")
	}
	if err := m.ResetWorkload(context.Background(), "app", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := backupCount(t, m, "app"); got != 0 {
		t.Fatalf("a runner, not a backed-up type, got %d backups on reset", got)
	}
}

func TestResetRefusedWhenItsBackupFails(t *testing.T) {
	m := newBackupMgr(t, config.Config{BackupBeforeReset: true})
	// A data dir the archive can't read makes the backup fail.
	w, _ := m.store.GetWorkload("db")
	w.DataDir = filepath.Join(m.cfg.DataRoot, "projects", "p", "missing")
	_ = m.store.PutWorkload(w)
	err := m.ResetWorkload(context.Background(), "db", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "nothing was wiped") {
		t.Fatalf("reset with a failed backup returned %v, want a refusal", err)
	}
}

// evictable adds a newer project and keeps one, so the reaper's only candidate is
// newBackupMgr's "p" (idle since creation).
func evictable(t *testing.T, m *Manager) {
	t.Helper()
	_ = m.store.PutProject(&store.Project{ID: "newer", LastActiveAt: time.Now()})
	if err := m.SetLRUKeepMax(1); err != nil {
		t.Fatal(err)
	}
}

func projectExists(m *Manager, id string) bool {
	_, err := m.store.GetProject(id)
	return err == nil
}

func TestEvictionBacksUpFirst(t *testing.T) {
	// The box's settings: databases only, a deleted one keeps its newest backup.
	m := newBackupMgr(t, config.Config{
		BackupTypes: []string{"tinbase-project"}, BackupRetainDeleted: 1, BackupBeforeEvict: true,
		LRUBatch: 25, LRUMinAge: time.Hour,
	})
	evictable(t, m)
	m.ReapLRU(context.Background())
	if projectExists(m, "p") {
		t.Fatal("p should have been evicted")
	}
	if got := backupCount(t, m, "db"); got != 1 {
		t.Fatalf("db backups after eviction = %d, want the 1 taken before the delete", got)
	}
	if got := backupCount(t, m, "app"); got != 0 {
		t.Fatalf("a runner, not a backed-up type, got %d backups on eviction", got)
	}
}

func TestEvictionSkipsABackupThatWouldHoldTheSameBytes(t *testing.T) {
	m := newBackupMgr(t, config.Config{
		BackupTypes: []string{"tinbase-project"}, BackupRetainDeleted: 1, BackupBeforeEvict: true,
		LRUBatch: 25, LRUMinAge: time.Hour,
	})
	if _, err := m.BackupWorkload(context.Background(), "db"); err != nil {
		t.Fatal(err)
	}
	backdate(t, m, "db") // the data predates its backup
	// The stub runtime reports every workload running, so the backup above brought
	// it back up; an idle project's workloads are suspended.
	_ = m.store.SetWorkloadState("db", runtime.StateSuspended)
	evictable(t, m)
	m.ReapLRU(context.Background())
	if projectExists(m, "p") {
		t.Fatal("p should have been evicted")
	}
	if got := backupCount(t, m, "db"); got != 1 {
		t.Fatalf("db backups = %d, want the 1 that already held this data", got)
	}
}

func TestEvictionWaitsWhenItsBackupFails(t *testing.T) {
	m := newBackupMgr(t, config.Config{BackupBeforeEvict: true, LRUBatch: 25, LRUMinAge: time.Hour})
	// A data dir the archive can't read makes the backup fail.
	w, _ := m.store.GetWorkload("db")
	w.DataDir = filepath.Join(m.cfg.DataRoot, "projects", "p", "missing")
	_ = m.store.PutWorkload(w)
	evictable(t, m)
	m.ReapLRU(context.Background())
	if !projectExists(m, "p") {
		t.Fatal("a project whose backup failed was deleted anyway")
	}
}

func TestEvictionDoesNotBackUpByDefault(t *testing.T) {
	m := newBackupMgr(t, config.Config{BackupRetainDeleted: -1, LRUBatch: 25, LRUMinAge: time.Hour})
	evictable(t, m)
	m.ReapLRU(context.Background())
	if projectExists(m, "p") {
		t.Fatal("p should have been evicted")
	}
	if got := backupCount(t, m, "db"); got != 0 {
		t.Fatalf("db backups = %d with BackupBeforeEvict off, want 0", got)
	}
}
