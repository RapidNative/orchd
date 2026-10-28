package manager

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
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
	// offline exports, of a stopped database in a one-off container
	offline     int
	failOffline bool
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
	return nil, r.writeExport(ref)
}

func (r *dumpRT) RunOffline(_ context.Context, ref, _, dataDir string, argv ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state[ref] == runtime.StateRunning {
		return nil, fmt.Errorf("offline run over a running database")
	}
	if dataDir != r.dataDirs[ref] || len(argv) < 3 || argv[2] != "export" {
		return nil, fmt.Errorf("unexpected offline run %s %v", dataDir, argv)
	}
	if r.failOffline {
		return nil, fmt.Errorf("offline export failed")
	}
	r.offline++
	r.checkpoint(ref) // Postgres started and stopped for the export
	return nil, r.writeExport(ref)
}

// writeExport writes a fake `tinbase db export` archive. Caller holds mu.
func (r *dumpRT) writeExport(ref string) error {
	man, _ := json.Marshal(map[string]any{
		"format": "tinbase-dump", "version": 1, "contentHash": r.content,
		"pristine": map[string]any{"pristine": r.pristine, "state": map[bool]string{true: "pristine", false: "diverged"}[r.pristine]},
	})
	out := filepath.Join(r.dataDirs[ref], dumpExportFile)
	_ = os.MkdirAll(filepath.Dir(out), 0o755)
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range map[string][]byte{"./manifest.json": man, "./dump.sql": []byte("-- " + r.content)} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write(body)
	}
	_ = tw.Close()
	_ = gz.Close()
	return f.Close()
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
	if cfg.BackupInterval == 0 {
		cfg.BackupInterval = 24 * time.Hour
	}
	_ = os.MkdirAll(filepath.Join(root, "state"), 0o755)
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

// wakeAndStop runs db once more after its dump and stops it without one.
func wakeAndStop(t *testing.T, m *Manager, rt *dumpRT) {
	t.Helper()
	_, _ = rt.Start(context.Background(), runtime.Spec{Ref: "db"})
	_ = rt.Suspend(context.Background(), "db")
	_ = m.store.SetWorkloadState("db", runtime.StateSuspended)
	w, _ := m.store.GetWorkload("db")
	if dumpCurrent(w) {
		t.Fatal("a database that ran after its dump still counts as dumped")
	}
}

func TestEvictionExportsAStaleDumpOffline(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{IdleTimeout: time.Minute, BackupBeforeEvict: true, BackupRetainDeleted: 1, LRUBatch: 25, LRUMinAge: time.Hour})
	rt.pristine, rt.content = false, "rows"
	idle(m, "db")
	m.ReapIdle(context.Background())

	time.Sleep(1100 * time.Millisecond) // backup ids have second resolution
	wakeAndStop(t, m, rt)
	rt.content = "rows-2" // it wrote while it ran
	longIdle(t, m)
	evictable(t, m)
	starts := rt.starts
	m.ReapLRU(context.Background())
	if projectExists(m, "p") {
		t.Fatal("p should have been evicted")
	}
	if rt.offline != 1 || rt.starts != starts {
		t.Fatalf("offline exports %d, boots %d: want one offline export and no boot", rt.offline, rt.starts-starts)
	}
	list, _ := m.ListBackups("db")
	if len(list) != 1 || list[0].Kind != backup.KindDump {
		t.Fatalf("backups after evicting a stale dump: %v, want the offline dump (retain 1)", kinds(t, m, "db"))
	}
}

func TestEvictionWaitsWhenTheOfflineExportFails(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{IdleTimeout: time.Minute, BackupBeforeEvict: true, BackupRetainDeleted: 1, LRUBatch: 25, LRUMinAge: time.Hour})
	rt.pristine, rt.content = false, "rows"
	idle(m, "db")
	m.ReapIdle(context.Background())
	wakeAndStop(t, m, rt)
	rt.failOffline = true
	longIdle(t, m)
	evictable(t, m)
	m.ReapLRU(context.Background())
	if !projectExists(m, "p") {
		t.Fatal("p was evicted without a backup of its last writes")
	}
	for _, k := range kinds(t, m, "db") {
		if k != "dump" {
			t.Fatalf("a database was backed up as %q", k)
		}
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

func TestManualBackupOfAStoppedDatabaseIsAnOfflineDump(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{})
	_ = rt.Suspend(context.Background(), "db")
	b, err := m.BackupWorkload(context.Background(), "db")
	if err != nil {
		t.Fatal(err)
	}
	w, _ := m.store.GetWorkload("db")
	if b.Kind != backup.KindDump || rt.offline != 1 || !dumpCurrent(w) {
		t.Fatalf("manual backup of a stopped database: %+v, offline %d, sealed %v", b, rt.offline, dumpCurrent(w))
	}
}

func TestSweepExportsASuspendedDatabaseOnce(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{})
	rt.pristine, rt.content = false, "rows"
	_ = rt.Suspend(context.Background(), "db") // suspended before dumps existed
	now := time.Now()
	if n := m.sweepBackups(context.Background(), now); n != 1 || rt.offline != 1 {
		t.Fatalf("first sweep took %d, offline %d", n, rt.offline)
	}
	w, _ := m.store.GetWorkload("db")
	if !dumpCurrent(w) {
		t.Fatal("the offline dump is not sealed")
	}
	if n := m.sweepBackups(context.Background(), now.Add(48*time.Hour)); n != 0 {
		t.Fatalf("a sealed database was backed up again (%d)", n)
	}
}

func TestSweepAgesSurviveARestart(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{})
	rt.pristine, rt.content = false, "rows"
	now := time.Now()
	if n := m.sweepBackups(context.Background(), now); n != 1 || rt.exports != 1 {
		t.Fatalf("first sweep took %d, exports %d", n, rt.exports)
	}
	restarted := New(m.cfg, m.store, rt)
	if n := restarted.sweepBackups(context.Background(), now.Add(time.Hour)); n != 0 {
		t.Fatalf("a restart made a database due an hour after its dump (%d)", n)
	}
	if n := restarted.sweepBackups(context.Background(), now.Add(25*time.Hour)); n != 1 || rt.exports != 2 {
		t.Fatalf("a running database 25 h after its dump: took %d, exports %d", n, rt.exports)
	}
}

func TestSweepWaitsAfterAFailedDump(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{})
	_ = rt.Suspend(context.Background(), "db")
	rt.failOffline = true
	now := time.Now()
	if n := m.sweepBackups(context.Background(), now); n != 0 || rt.offline != 0 {
		t.Fatalf("a failed dump counted as taken (%d)", n)
	}
	if n := m.sweepBackups(context.Background(), now.Add(time.Minute)); n != 0 {
		t.Fatalf("a failed dump was retried a minute later (%d)", n)
	}
	c, err := m.Coverage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Totals[CoverFailed] != 1 || c.SafeToWipe || c.Workloads[0].Error == "" {
		t.Fatalf("coverage after a failed dump: %+v", c)
	}
	rt.failOffline = false
	if n := m.sweepBackups(context.Background(), now.Add(2*time.Hour)); n != 1 || rt.offline != 1 {
		t.Fatalf("retry after an hour: took %d, offline %d", n, rt.offline)
	}
}

func TestSweepTakesABatchAtATime(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{BackupSweepBatch: 1})
	dir := filepath.Join(m.cfg.DataRoot, "projects", "p", "db2")
	_ = os.MkdirAll(dir, 0o755)
	rt.dataDirs["db2"] = dir
	_ = m.store.PutWorkload(&store.Workload{ID: "db2", ProjectID: "p", Type: runtime.WorkloadTinbaseProject, State: runtime.StateRunning, DataDir: dir})
	now := time.Now()
	if n := m.sweepBackups(context.Background(), now); n != 1 {
		t.Fatalf("first sweep took %d, want the batch of 1", n)
	}
	if n := m.sweepBackups(context.Background(), now); n != 1 || rt.exports != 2 {
		t.Fatalf("second sweep took %d, exports %d", n, rt.exports)
	}
}

func TestCoverageAndDrain(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{})
	rt.pristine, rt.content = false, "rows"
	_ = rt.Suspend(context.Background(), "db")
	c, err := m.Coverage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.SafeToWipe || c.Totals[CoverStale] != 1 {
		t.Fatalf("an undumped database: %+v", c)
	}
	c, err = m.DrainBackups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !c.SafeToWipe || c.Totals[CoverCurrent] != 1 || c.StateBackupAt == nil || c.Workloads[0].BackupID == "" {
		t.Fatalf("after the drain: %+v", c)
	}
	_, _ = rt.Start(context.Background(), runtime.Spec{Ref: "db"})
	if c, _ = m.Coverage(context.Background()); c.Totals[CoverRecent] != 1 || !c.SafeToWipe {
		t.Fatalf("running after a fresh dump: %+v", c)
	}
}

func TestBackupTargetFromConfig(t *testing.T) {
	m, _ := newDumpMgr(t, config.Config{BackupS3Endpoint: "http://127.0.0.1:1", BackupS3Bucket: "b", BackupS3AccessKey: "k", BackupS3SecretKey: "s", BackupS3Prefix: "staging"})
	if got := m.GetBackupTarget(); got.Type != "s3" || got.Bucket != "b" || got.Prefix != "staging" {
		t.Fatalf("target: %+v", got)
	}
	if _, ok := m.backupStore().(*backup.S3Store); !ok {
		t.Fatalf("store is %T, want s3", m.backupStore())
	}
	if err := m.SetBackupTarget(store.BackupTarget{Type: "local"}); !errors.Is(err, ErrBackupTargetFromConfig) {
		t.Fatalf("SetBackupTarget over a config target: %v", err)
	}
}

func TestSweepWaitsWhileTheDiskIsBusy(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{})
	psi := filepath.Join(t.TempDir(), "io")
	old := ioPressureFile
	ioPressureFile = psi
	t.Cleanup(func() { ioPressureFile = old })

	_ = os.WriteFile(psi, []byte("some avg10=90.00 avg60=80.00 avg300=40.00 total=1\nfull avg10=85.00 avg60=80.00 avg300=40.00 total=1\n"), 0o644)
	if n := m.sweepBackups(context.Background(), time.Now()); n != 0 || rt.exports != 0 {
		t.Fatalf("a sweep under IO pressure took %d", n)
	}
	_ = os.WriteFile(psi, []byte("some avg10=3.00 avg60=2.00 avg300=1.00 total=1\nfull avg10=0.40 avg60=0.20 avg300=0.10 total=1\n"), 0o644)
	if n := m.sweepBackups(context.Background(), time.Now()); n != 1 || rt.exports != 1 {
		t.Fatalf("a sweep on a quiet disk took %d", n)
	}
}

func TestFailuresDoNotFillTheBatch(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{BackupSweepBatch: 1})
	rt.failExec = true // "db" runs an image that can't export
	dir := filepath.Join(m.cfg.DataRoot, "projects", "p", "db2")
	_ = os.MkdirAll(dir, 0o755)
	rt.dataDirs["db2"] = dir
	rt.state["db2"] = runtime.StateSuspended
	_ = m.store.PutWorkload(&store.Workload{ID: "db2", ProjectID: "p", Type: runtime.WorkloadTinbaseProject, State: runtime.StateSuspended, DataDir: dir})
	if n := m.sweepBackups(context.Background(), time.Now()); n != 1 || rt.offline != 1 {
		t.Fatalf("a failing database used the batch of 1: took %d, offline %d", n, rt.offline)
	}
}

func TestSweepSkipsADatabaseEvictedAfterItListed(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{})
	_ = rt.Suspend(context.Background(), "db")
	w, _ := m.store.GetWorkload("db") // the sweep's list, taken before the eviction
	if err := m.DeleteProject(context.Background(), "p"); err != nil {
		t.Fatal(err)
	}
	if err := m.dailyDump(context.Background(), w); !errors.Is(err, errWorkloadGone) {
		t.Fatalf("dump of an evicted database: %v", err)
	}
	if rt.offline != 0 {
		t.Fatal("exported an evicted database")
	}
	if _, err := os.Stat(w.DataDir); !os.IsNotExist(err) {
		t.Fatalf("the evicted data dir is back: %v", err)
	}
	if _, failed := m.backupRuns.failure("db"); failed {
		t.Fatal("an evicted database was recorded as a failed dump")
	}
}

func TestOfflineExportNeedsTheDataDir(t *testing.T) {
	m, rt := newDumpMgr(t, config.Config{})
	_ = rt.Suspend(context.Background(), "db")
	w, _ := m.store.GetWorkload("db")
	_ = os.RemoveAll(w.DataDir)
	if err := m.dailyDump(context.Background(), w); err == nil || rt.offline != 0 {
		t.Fatalf("exported a missing data dir: err %v, offline %d", err, rt.offline)
	}
}
