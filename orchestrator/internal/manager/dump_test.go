package manager

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/backup"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// dumpRT is a runtime whose containers answer `tinbase db export` with a fake
// export, and whose databases rewrite pg_control on every stop and start, as
// Postgres does at its checkpoints.
type dumpRT struct {
	reapStub
	mu       sync.Mutex
	dataDirs map[string]string // workload id -> data dir
	state    map[string]runtime.State
	content  string // what the database holds; the export's contentHash
	pristine bool
	exports  int
	failExec bool
	starts   int
}

func (r *dumpRT) Status(_ context.Context, id string) (runtime.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.state[id]; ok {
		return s, nil
	}
	return runtime.StateRunning, nil
}

func (r *dumpRT) checkpoint(id string) {
	p := filepath.Join(r.dataDirs[id], pgControlFile)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(fmt.Sprintf("checkpoint %d", time.Now().UnixNano())), 0o600)
}

func (r *dumpRT) Suspend(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state[id] = runtime.StateSuspended
	r.checkpoint(id) // shutdown checkpoint
	return nil
}

func (r *dumpRT) Start(_ context.Context, spec runtime.Spec) (*runtime.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state[spec.Ref] = runtime.StateRunning
	r.starts++
	r.checkpoint(spec.Ref) // end-of-recovery checkpoint
	return &runtime.Instance{}, nil
}

func (r *dumpRT) ExecInContainer(_ context.Context, ref, user string, argv ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failExec {
		return nil, fmt.Errorf("exec failed")
	}
	if user != dumpUser || len(argv) < 3 || argv[0] != "tinbase" || argv[2] != "export" {
		return nil, fmt.Errorf("unexpected exec %s %v", user, argv)
	}
	r.exports++
	man, _ := json.Marshal(map[string]any{
		"format": "tinbase-dump", "version": 1, "contentHash": r.content,
		"pristine": map[string]any{"pristine": r.pristine, "state": map[bool]string{true: "pristine", false: "diverged"}[r.pristine]},
	})
	out := filepath.Join(r.dataDirs[ref], dumpExportFile)
	_ = os.MkdirAll(filepath.Dir(out), 0o755)
	f, err := os.Create(out)
	if err != nil {
		return nil, err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range map[string][]byte{"./manifest.json": man, "./dump.sql": []byte("-- " + r.content)} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write(body)
	}
	_ = tw.Close()
	_ = gz.Close()
	return nil, f.Close()
}

// newDumpMgr is newBackupMgr's box with database dumps on: "db" runs.
func newDumpMgr(t *testing.T, cfg config.Config) (*Manager, *dumpRT) {
	t.Helper()
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cfg.DataRoot = root
	cfg.BackupDir = filepath.Join(root, "backups")
	cfg.BackupTypes = []string{"tinbase-project"}
	cfg.BackupDumpTypes = []string{"tinbase-project"}
	if cfg.BackupRetain == 0 {
		cfg.BackupRetain = 7
	}
	rt := &dumpRT{dataDirs: map[string]string{}, state: map[string]runtime.State{}, content: "seed", pristine: true}
	m := New(cfg, st, rt)
	_ = st.PutProject(&store.Project{ID: "p"})
	dir := filepath.Join(root, "projects", "p", "db")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rt.dataDirs["db"] = dir
	rt.checkpoint("db")
	_ = st.PutWorkload(&store.Workload{ID: "db", ProjectID: "p", Type: runtime.WorkloadTinbaseProject, State: runtime.StateRunning, DataDir: dir})
	return m, rt
}

func kinds(t *testing.T, m *Manager, id string) []string {
	t.Helper()
	list, err := m.ListBackups(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, b := range list {
		k := b.Kind
		if k == "" {
			k = "dir"
		}
		out = append(out, k)
	}
	return out
}

// longIdle makes project p look idle for two hours, past LRUMinAge: going idle
// stamps it as just active.
func longIdle(t *testing.T, m *Manager) {
	t.Helper()
	p, err := m.store.GetProject("p")
	if err != nil {
		t.Fatal(err)
	}
	p.LastActiveAt = time.Now().Add(-2 * time.Hour)
	_ = m.store.PutProject(p)
}

func idle(m *Manager, id string) {
	m.markLive(id, "127.0.0.1:1")
	m.mu.Lock()
	m.live[id].lastSeen = time.Now().Add(-time.Hour)
	m.mu.Unlock()
}

func TestDumpKeepsNothingForAPristineDatabase(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{})
	m.backupAll(context.Background())
	if got := kinds(t, m, "db"); len(got) != 0 {
		t.Fatalf("pristine database stored %v", got)
	}
	if rt.exports != 1 {
		t.Fatalf("exports = %d, want 1", rt.exports)
	}
}

func TestDumpStoresOnlyChangedContent(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{})
	rt.pristine, rt.content = false, "rows-1"
	m.backupAll(context.Background())
	m.backupAll(context.Background()) // same content: nothing new
	if got := kinds(t, m, "db"); len(got) != 1 || got[0] != "dump" {
		t.Fatalf("after two identical dumps: %v, want one dump", got)
	}
	time.Sleep(1100 * time.Millisecond) // backup ids have second resolution
	rt.content = "rows-2"
	m.backupAll(context.Background())
	if got := kinds(t, m, "db"); len(got) != 2 {
		t.Fatalf("after a change: %v, want two dumps", got)
	}
}

func TestIdleDumpIsSealedAndEvictionTrustsIt(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{IdleTimeout: time.Minute, BackupBeforeEvict: true, BackupRetainDeleted: 1, LRUBatch: 25, LRUMinAge: time.Hour})
	rt.pristine, rt.content = false, "rows"
	idle(m, "db")
	m.ReapIdle(context.Background())
	w, _ := m.store.GetWorkload("db")
	if w.State != runtime.StateSuspended {
		t.Fatalf("db not suspended: %s", w.State)
	}
	if !dumpCurrent(w) {
		t.Fatal("the dump taken before suspending is not sealed as current")
	}
	if got := kinds(t, m, "db"); len(got) != 1 || got[0] != "dump" {
		t.Fatalf("backups after going idle: %v", got)
	}

	longIdle(t, m)
	evictable(t, m)
	m.ReapLRU(context.Background())
	if projectExists(m, "p") {
		t.Fatal("p should have been evicted")
	}
	if got := kinds(t, m, "db"); len(got) != 1 || got[0] != "dump" {
		t.Fatalf("eviction should rely on the sealed dump, backups: %v", got)
	}
}

func TestEvictionFallsBackToATarWhenTheDumpIsStale(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{IdleTimeout: time.Minute, BackupBeforeEvict: true, BackupRetainDeleted: 1, LRUBatch: 25, LRUMinAge: time.Hour})
	rt.pristine, rt.content = false, "rows"
	idle(m, "db")
	m.ReapIdle(context.Background())

	// it ran again after the dump (a wake), and went down without one
	time.Sleep(1100 * time.Millisecond) // backup ids have second resolution
	w, _ := m.store.GetWorkload("db")
	_, _ = rt.Start(context.Background(), runtime.Spec{Ref: "db"})
	_ = rt.Suspend(context.Background(), "db")
	_ = m.store.SetWorkloadState("db", runtime.StateSuspended)
	if dumpCurrent(w) {
		t.Fatal("a database that ran after its dump still counts as dumped")
	}
	longIdle(t, m)
	evictable(t, m)
	m.ReapLRU(context.Background())
	if projectExists(m, "p") {
		t.Fatal("p should have been evicted")
	}
	got := kinds(t, m, "db")
	if len(got) != 1 || got[0] != "dir" {
		t.Fatalf("backups after evicting a stale dump: %v, want the fallback tar (retain 1)", got)
	}
}

func TestFailedIdleDumpStillSuspends(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{IdleTimeout: time.Minute})
	rt.failExec = true
	idle(m, "db")
	m.ReapIdle(context.Background())
	w, _ := m.store.GetWorkload("db")
	if w.State != runtime.StateSuspended {
		t.Fatalf("a failed dump kept db %s", w.State)
	}
	if dumpCurrent(w) {
		t.Fatal("a failed dump was sealed")
	}
}

func TestManualBackupOfARunningDatabaseIsAForcedDump(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{})
	b, err := m.BackupWorkload(context.Background(), "db")
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind != backup.KindDump || rt.exports != 1 {
		t.Fatalf("manual backup: %+v, exports %d", b, rt.exports)
	}
	if st, _ := rt.Status(context.Background(), "db"); st != runtime.StateRunning {
		t.Fatalf("a dump paused the database: %s", st)
	}
}

func TestRestoringADumpLeavesItForTinbaseToLoad(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{})
	rt.pristine, rt.content = false, "rows"
	b, err := m.BackupWorkload(context.Background(), "db")
	if err != nil {
		t.Fatal(err)
	}
	starts := rt.starts
	if err := m.RestoreWorkload(context.Background(), "db", b.ID); err != nil {
		t.Fatal(err)
	}
	w, _ := m.store.GetWorkload("db")
	if _, err := os.Stat(filepath.Join(w.DataDir, dumpRestoreFile)); err != nil {
		t.Fatalf("restore archive not in place: %v", err)
	}
	if rt.starts != starts+1 || w.State != runtime.StateRunning {
		t.Fatalf("restore did not boot the workload: starts %d, state %s", rt.starts, w.State)
	}
}

func TestS3TargetKeepsReadingLocalArchives(t *testing.T) {
	m, _ := newDumpMgr(t, config.Config{})
	if err := os.MkdirAll(m.cfg.BackupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bs, err := m.buildBackupStore(store.BackupTarget{Type: "s3", Endpoint: "http://127.0.0.1:1", Bucket: "b", AccessKey: "k", SecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := bs.(*backup.MultiStore); !ok {
		t.Fatalf("s3 target with local archives on disk is %T, want a MultiStore", bs)
	}
}
