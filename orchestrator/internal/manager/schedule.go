package manager

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/backup"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// Scheduled backups keep one rule: every database is pristine, or has a dump in
// the store that is its exact contents (sealed when it stopped) or under
// BackupInterval old. Then the box holds nothing that can't be rebuilt, and can
// be wiped once the drain has run (see DrainBackups).
//
// The scheduler sweeps every BackupSweep and backs up only what is due. Ages
// come from each database's dump state in its data dir, not a timer, so an
// orchd restart doesn't push a backup back: the old 24 h ticker started over on
// every deploy and skipped days.

// dumpRetryAfter spaces out retries of a database whose dump failed.
const dumpRetryAfter = time.Hour

// ioBusyFull is the share of time all tasks stalled on IO (Linux PSI "full",
// avg10) at which a sweep stops for now: the boxes' disks stall under bulk IO
// well before throughput looks high, and wakes then fail.
const ioBusyFull = 20.0

// ioPressureFile is where the kernel reports IO pressure; absent off Linux.
var ioPressureFile = "/proc/pressure/io"

// ioBusy reports whether the disk is under pressure now.
func ioBusy() bool {
	b, err := os.ReadFile(ioPressureFile)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "full ") {
			continue
		}
		for _, f := range strings.Fields(line) {
			if v, ok := strings.CutPrefix(f, "avg10="); ok {
				n, err := strconv.ParseFloat(v, 64)
				return err == nil && n >= ioBusyFull
			}
		}
	}
	return false
}

// backupRuns is what the scheduler remembers between sweeps. All of it can be
// lost: a restart retries failures and looks the rest up again.
type backupRuns struct {
	mu      sync.Mutex
	failed  map[string]dumpFailure // workload id -> its last dump, while that failed
	checked map[string]time.Time   // tar-backed workload id -> newest backup, or when found unchanged since
	stateAt time.Time              // newest control-plane snapshot
}

type dumpFailure struct {
	At  time.Time
	Err string
}

func newBackupRuns() *backupRuns {
	return &backupRuns{failed: map[string]dumpFailure{}, checked: map[string]time.Time{}}
}

// dumped records the outcome of a dump.
func (r *backupRuns) dumped(id string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		delete(r.failed, id)
		return
	}
	r.failed[id] = dumpFailure{At: time.Now(), Err: err.Error()}
}

func (r *backupRuns) failure(id string) (dumpFailure, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.failed[id]
	return f, ok
}

// checkedAt returns when a tar-backed workload was last backed up or found
// unchanged, looking up its newest backup the first time.
func (r *backupRuns) checkedAt(id string, newest func() time.Time) time.Time {
	r.mu.Lock()
	at, ok := r.checked[id]
	r.mu.Unlock()
	if ok {
		return at
	}
	at = newest()
	r.check(id, at)
	return at
}

func (r *backupRuns) check(id string, at time.Time) {
	r.mu.Lock()
	r.checked[id] = at
	r.mu.Unlock()
}

func newestBackup(bk backup.Store, id string) time.Time {
	list, err := bk.List(id)
	if err != nil || len(list) == 0 {
		return time.Time{}
	}
	return list[0].CreatedAt // newest first
}

// RunBackupScheduler takes the backups that fall due, sweeping every
// BackupSweep until ctx is done. BackupInterval 0 turns it off.
func (m *Manager) RunBackupScheduler(ctx context.Context) {
	if m.cfg.BackupInterval <= 0 {
		return
	}
	every := m.cfg.BackupSweep
	if every <= 0 {
		every = 10 * time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sweepBackups(ctx, time.Now())
		}
	}
}

// sweepBackups backs up the workloads that are due, one at a time and at most
// BackupSweepBatch of them, then the control-plane state when it is due.
// Returns how many workloads it backed up or tried to.
func (m *Manager) sweepBackups(ctx context.Context, now time.Time) int {
	bk := m.backupStore()
	if bk == nil {
		return 0
	}
	batch := m.cfg.BackupSweepBatch
	if batch <= 0 {
		batch = 20
	}
	n := 0
	for _, w := range m.store.ListWorkloads("") {
		if ctx.Err() != nil {
			return n
		}
		if n >= batch {
			log.Printf("backup sweep: took %d, the rest wait for the next sweep", n)
			break
		}
		if !m.backsUp(w.Type) {
			continue
		}
		due := false
		if m.dumps(w.Type) {
			due = m.dumpDue(ctx, w, now)
		} else {
			due = m.tarDue(bk, w, now)
		}
		if !due {
			continue
		}
		if ioBusy() {
			log.Printf("backup sweep: disk busy, the rest wait for the next sweep (took %d)", n)
			return n
		}
		n++
		if m.dumps(w.Type) {
			_ = m.dailyDump(ctx, w)
		} else {
			m.tarBackup(ctx, w)
		}
	}
	if now.Sub(m.stateBackupAt(bk)) >= m.cfg.BackupInterval {
		m.stateBackup(ctx)
	}
	return n
}

// dumpDue reports whether a database needs a dump now: it has never had one, it
// stopped without a sealed one, or it runs and its last one is BackupInterval
// old. One whose dump just failed waits dumpRetryAfter.
func (m *Manager) dumpDue(ctx context.Context, w *store.Workload, now time.Time) bool {
	if dumpCurrent(w) {
		return false
	}
	if f, ok := m.backupRuns.failure(w.ID); ok && now.Sub(f.At) < dumpRetryAfter {
		return false
	}
	st, err := readDumpState(w)
	if err != nil {
		return true
	}
	if s, _ := m.rt.Status(ctx, w.ID); s != runtime.StateRunning {
		return true
	}
	return now.Sub(st.At) >= m.cfg.BackupInterval
}

// tarDue reports whether a workload backed up as a tar of its data dir needs a
// new one: its newest is BackupInterval old and its data changed since.
func (m *Manager) tarDue(bk backup.Store, w *store.Workload, now time.Time) bool {
	at := m.backupRuns.checkedAt(w.ID, func() time.Time { return newestBackup(bk, w.ID) })
	if now.Sub(at) < m.cfg.BackupInterval {
		return false
	}
	if unchangedSinceBackup(bk, w) {
		m.backupRuns.check(w.ID, now)
		return false
	}
	return true
}

func (m *Manager) tarBackup(ctx context.Context, w *store.Workload) {
	b, err := m.BackupWorkload(ctx, w.ID)
	if err != nil {
		log.Printf("backup %s: %v", w.ID, err)
		return
	}
	m.backupRuns.check(w.ID, b.CreatedAt)
	log.Printf("backup %s -> %s (%d bytes)", w.ID, b.ID, b.SizeBytes)
}

func (m *Manager) stateBackupAt(bk backup.Store) time.Time {
	m.backupRuns.mu.Lock()
	at := m.backupRuns.stateAt
	m.backupRuns.mu.Unlock()
	if at.IsZero() {
		at = newestBackup(bk, StateBackupKey)
		m.backupRuns.mu.Lock()
		m.backupRuns.stateAt = at
		m.backupRuns.mu.Unlock()
	}
	return at
}

func (m *Manager) stateBackup(ctx context.Context) {
	b, err := m.BackupState(ctx)
	if err != nil {
		log.Printf("backup state: %v", err)
		return
	}
	m.backupRuns.mu.Lock()
	m.backupRuns.stateAt = b.CreatedAt
	m.backupRuns.mu.Unlock()
	log.Printf("backup state -> %s (%d bytes)", b.ID, b.SizeBytes)
}

// backupAll backs up every backed-up workload now, whatever its age: a database
// not covered by a sealed dump is dumped (offline when stopped), any other
// workload changed since its newest backup is archived; then the control-plane
// state. A failure is logged and the rest go on.
func (m *Manager) backupAll(ctx context.Context) {
	bk := m.backupStore()
	for _, w := range m.store.ListWorkloads("") {
		if ctx.Err() != nil {
			return
		}
		if !m.backsUp(w.Type) {
			continue
		}
		if m.dumps(w.Type) {
			_ = m.dailyDump(ctx, w)
			continue
		}
		if bk != nil && unchangedSinceBackup(bk, w) {
			continue
		}
		m.tarBackup(ctx, w)
	}
	m.stateBackup(ctx)
}

// Coverage statuses of a backed-up workload.
const (
	// CoverCurrent: its newest backup is its exact contents (a database's dump
	// sealed when it stopped, or a tar with nothing written since).
	CoverCurrent = "current"
	// CoverRecent: it runs, and its newest backup is under BackupInterval old.
	CoverRecent = "recent"
	// CoverStale: no backup, or one older than BackupInterval that it has
	// changed since. Wiping the box would lose data.
	CoverStale = "stale"
	// CoverFailed: its last dump failed and nothing newer covers it.
	CoverFailed = "failed"
)

// WorkloadCoverage is one workload's line in the coverage report.
type WorkloadCoverage struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Type      string `json:"type"`
	State     string `json:"state"`
	Status    string `json:"status"`
	// Pristine: the last dump held only migrations and seed, so none is stored.
	Pristine bool       `json:"pristine,omitempty"`
	BackupAt *time.Time `json:"backup_at,omitempty"`
	BackupID string     `json:"backup_id,omitempty"`
	Error    string     `json:"error,omitempty"`
}

// BackupCoverage reports whether the box holds anything that can't be rebuilt.
type BackupCoverage struct {
	At time.Time `json:"at"`
	// SafeToWipe: backups are on, the control-plane state is backed up, and no
	// workload is stale or failed. A running database ("recent") still loses
	// what it wrote after its dump: drain right before turning the box off.
	SafeToWipe    bool               `json:"safe_to_wipe"`
	Totals        map[string]int     `json:"totals"`
	StateBackupAt *time.Time         `json:"state_backup_at,omitempty"`
	Workloads     []WorkloadCoverage `json:"workloads"`
}

// Coverage reports, for every backed-up workload, whether its data is in the
// backup store. Workloads of other types (runners, rebuilt from the project's
// files) are only counted, as not_backed_up.
func (m *Manager) Coverage(ctx context.Context) (BackupCoverage, error) {
	bk := m.backupStore()
	if bk == nil {
		return BackupCoverage{}, ErrBackupsDisabled
	}
	now := time.Now().UTC()
	c := BackupCoverage{At: now, Totals: map[string]int{}, Workloads: []WorkloadCoverage{}}
	for _, w := range m.store.ListWorkloads("") {
		if !m.backsUp(w.Type) {
			c.Totals["not_backed_up"]++
			continue
		}
		wc := WorkloadCoverage{ID: w.ID, ProjectID: w.ProjectID, Type: string(w.Type), State: string(w.State)}
		if m.dumps(w.Type) {
			m.coverDump(ctx, w, now, &wc)
		} else {
			m.coverTar(bk, w, now, &wc)
		}
		c.Totals[wc.Status]++
		c.Workloads = append(c.Workloads, wc)
	}
	if at := newestBackup(bk, StateBackupKey); !at.IsZero() {
		c.StateBackupAt = &at
	}
	c.SafeToWipe = c.StateBackupAt != nil && c.Totals[CoverStale] == 0 && c.Totals[CoverFailed] == 0
	return c, nil
}

func (m *Manager) coverDump(ctx context.Context, w *store.Workload, now time.Time, wc *WorkloadCoverage) {
	f, failed := m.backupRuns.failure(w.ID)
	if failed {
		wc.Error = f.Err
	}
	st, err := readDumpState(w)
	if err == nil {
		at := st.At
		wc.BackupAt, wc.BackupID, wc.Pristine = &at, st.BackupID, st.Pristine
	}
	// A sealed database has not run since its dump (starting rewrites
	// pg_control), so only the others are asked whether they run.
	if dumpCurrent(w) {
		wc.Status = CoverCurrent
		return
	}
	s, _ := m.rt.Status(ctx, w.ID)
	running := s == runtime.StateRunning
	switch {
	case running && err == nil && now.Sub(st.At) < m.cfg.BackupInterval:
		wc.Status = CoverRecent
	case failed:
		wc.Status = CoverFailed
	default:
		wc.Status = CoverStale
	}
}

func (m *Manager) coverTar(bk backup.Store, w *store.Workload, now time.Time, wc *WorkloadCoverage) {
	list, err := bk.List(w.ID)
	if err == nil && len(list) > 0 {
		at := list[0].CreatedAt
		wc.BackupAt, wc.BackupID = &at, list[0].ID
	}
	switch {
	case wc.BackupAt != nil && unchangedSinceBackup(bk, w):
		wc.Status = CoverCurrent
	case wc.BackupAt != nil && now.Sub(*wc.BackupAt) < m.cfg.BackupInterval:
		wc.Status = CoverRecent
	default:
		wc.Status = CoverStale
	}
}

// DrainBackups backs up everything not already covered, whatever its age (a
// running database is dumped again), and the control-plane state, then reports
// coverage. Run it right before turning a box off.
func (m *Manager) DrainBackups(ctx context.Context) (BackupCoverage, error) {
	if !m.BackupsEnabled() {
		return BackupCoverage{}, ErrBackupsDisabled
	}
	m.backupAll(ctx)
	return m.Coverage(ctx)
}
