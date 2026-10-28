package manager

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/backup"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// Dump backups (config.BackupDumpTypes) keep a tinbase export of a database
// instead of a tar of its data dir: `tinbase db export`, run inside the
// workload's own container, writes a logical dump of KBs where the data dir is
// tens of MB, without pausing the database (pg_dump reads one snapshot).
//
// One is taken when the workload goes idle, while it is still running, and once
// a day while it runs. Nothing is stored when the database holds only what its
// migrations and seed put there (tinbase's seed snapshot): re-creating it from
// the project's files gives the same database. Nor when the dump holds exactly
// what the last stored one does.
//
// After the idle-time dump the database stops, and the fingerprint of its
// pg_control is recorded beside the dump's state. Postgres rewrites pg_control
// at every checkpoint, startup and shutdown included, so while the fingerprint
// still matches the database has not run since: eviction can rely on that dump
// without starting it again.
//
// A stopped database without such a dump (suspended before dumps existed, its
// idle-time dump failed, or its image is older than tinbase 0.22.0) is exported
// offline: a one-off container of BackupDumpImage, no network, with the data
// dir mounted, while the workload stays down. Its dump is sealed the same way.
// A database is never backed up as a tar of its data dir.
//
// A dump is restored by tinbase itself: the archive goes to
// .tinbase/restore.tar.gz in the emptied data dir and the next boot loads it.

const (
	// Paths inside the tinbase image's container (images/tinbase/Dockerfile).
	dumpUser       = "node"
	dumpProjectDir = "/data"
	dumpDataDir    = "/data/db"

	// Relative to the workload's data dir, on the host and in the container.
	dumpExportFile  = ".tinbase/orchd-export.tar.gz"
	dumpRestoreFile = ".tinbase/restore.tar.gz"
	dumpStateFile   = ".orchd-dump.json"
	pgControlFile   = "db/global/pg_control"
)

// execer runs a command inside a workload's running container.
type execer interface {
	ExecInContainer(ctx context.Context, ref, user string, argv ...string) ([]byte, error)
}

// offliner runs a command in a one-off container over a stopped workload's
// data dir (runtime.DockerDriver.RunOffline).
type offliner interface {
	RunOffline(ctx context.Context, ref, image, dataDir string, argv ...string) ([]byte, error)
}

// dumpManifest is the part of a tinbase export's manifest.json orchd reads.
type dumpManifest struct {
	ContentHash string `json:"contentHash"`
	Pristine    struct {
		Pristine bool   `json:"pristine"`
		State    string `json:"state"`
		Reason   string `json:"reason"`
	} `json:"pristine"`
}

// dumpState is what the last dump left in the workload's data dir.
type dumpState struct {
	At          time.Time `json:"at"`
	ContentHash string    `json:"content_hash"`
	Pristine    bool      `json:"pristine"`
	// BackupID is the stored backup holding this content; empty when pristine.
	BackupID string `json:"backup_id,omitempty"`
	// PgControl fingerprints pg_control once the database stopped after the dump.
	PgControl string `json:"pg_control,omitempty"`
}

// dumps reports whether workloads of this type are backed up as tinbase exports.
func (m *Manager) dumps(t runtime.WorkloadType) bool {
	if _, ok := m.rt.(execer); !ok {
		return false
	}
	for _, x := range m.cfg.BackupDumpTypes {
		if x == string(t) {
			return true
		}
	}
	return false
}

func readDumpState(w *store.Workload) (*dumpState, error) {
	b, err := os.ReadFile(filepath.Join(w.DataDir, dumpStateFile))
	if err != nil {
		return nil, err
	}
	var st dumpState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func writeDumpState(w *store.Workload, st *dumpState) error {
	b, _ := json.Marshal(st)
	tmp := filepath.Join(w.DataDir, dumpStateFile+".partial")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(w.DataDir, dumpStateFile))
}

func pgControlFingerprint(w *store.Workload) (string, error) {
	b, err := os.ReadFile(filepath.Join(w.DataDir, pgControlFile))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// dumpCurrent reports whether the last dump still holds the database's exact
// contents: it was sealed when the database stopped, and the database has not
// run since.
func dumpCurrent(w *store.Workload) bool {
	st, err := readDumpState(w)
	if err != nil || st.PgControl == "" {
		return false
	}
	fp, err := pgControlFingerprint(w)
	return err == nil && fp == st.PgControl
}

// sealDump records pg_control after the database stopped following a dump.
func sealDump(w *store.Workload) error {
	st, err := readDumpState(w)
	if err != nil {
		return err
	}
	if st.PgControl, err = pgControlFingerprint(w); err != nil {
		return err
	}
	return writeDumpState(w, st)
}

// readDumpManifest reads manifest.json out of a tinbase export.
func readDumpManifest(file string) (dumpManifest, error) {
	var man dumpManifest
	f, err := os.Open(file)
	if err != nil {
		return man, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return man, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return man, errors.New("export has no manifest.json")
		}
		if err != nil {
			return man, err
		}
		if path.Clean(hdr.Name) == "manifest.json" {
			if err := json.NewDecoder(tr).Decode(&man); err != nil {
				return man, err
			}
			if man.ContentHash == "" {
				return man, errors.New("export manifest has no contentHash (tinbase older than 0.22?)")
			}
			return man, nil
		}
	}
}

// exportDump runs `tinbase db export` over the workload's database and returns
// the archive's host path and manifest: inside its container when running, else
// offline (see offliner). The caller removes the file and holds the refLock.
func (m *Manager) exportDump(ctx context.Context, w *store.Workload, running bool) (string, dumpManifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	host := filepath.Join(w.DataDir, dumpExportFile)
	_ = os.Remove(host)
	argv := []string{"tinbase", "db", "export",
		"-o", path.Join(dumpProjectDir, dumpExportFile),
		"--dir", dumpProjectDir, "--data-dir", dumpDataDir, "--engine", "native"}
	var err error
	switch {
	case running:
		ex, ok := m.rt.(execer)
		if !ok {
			return "", dumpManifest{}, fmt.Errorf("runtime %s cannot run commands in a workload", m.rt.Name())
		}
		_, err = ex.ExecInContainer(ctx, w.ID, dumpUser, argv...)
	default:
		off, ok := m.rt.(offliner)
		if !ok {
			return "", dumpManifest{}, fmt.Errorf("runtime %s cannot export a stopped database", m.rt.Name())
		}
		// Postgres initializes an empty cluster in a data dir that isn't there.
		if _, err := os.Stat(w.DataDir); err != nil {
			return "", dumpManifest{}, fmt.Errorf("no data dir to export: %w", err)
		}
		_, err = off.RunOffline(ctx, w.ID, m.cfg.BackupDumpImage, w.DataDir, argv...)
	}
	if err != nil {
		return "", dumpManifest{}, err
	}
	man, err := readDumpManifest(host)
	if err != nil {
		_ = os.Remove(host)
		return "", dumpManifest{}, err
	}
	return host, man, nil
}

// hasBackup reports whether id is still in the store.
func hasBackup(bk backup.Store, workloadID, id string) bool {
	list, err := bk.List(workloadID)
	if err != nil {
		return false
	}
	for _, b := range list {
		if b.ID == id {
			return true
		}
	}
	return false
}

// backupDump exports a workload's database and stores the archive, unless it is
// pristine or identical to the last stored one (force stores it anyway). A
// stopped database is exported offline and its dump sealed. Returns the stored
// backup, or nil with the reason nothing was stored. The outcome is recorded for
// the coverage report. The caller holds the workload's refLock.
func (m *Manager) backupDump(ctx context.Context, w *store.Workload, force bool) (*backup.Backup, string, error) {
	b, note, err := m.backupDumpOnce(ctx, w, force)
	m.backupRuns.dumped(w.ID, err)
	return b, note, err
}

func (m *Manager) backupDumpOnce(ctx context.Context, w *store.Workload, force bool) (*backup.Backup, string, error) {
	bk := m.backupStore()
	if bk == nil {
		return nil, "", ErrBackupsDisabled
	}
	st, _ := m.rt.Status(ctx, w.ID)
	running := st == runtime.StateRunning
	file, man, err := m.exportDump(ctx, w, running)
	if err != nil {
		return nil, "", err
	}
	defer os.Remove(file)
	stored, note, err := m.storeDump(w, bk, file, man, force)
	if err != nil || running {
		return stored, note, err
	}
	// The export started Postgres on the stopped data dir and shut it down,
	// so this dump is its exact contents until it next runs.
	if err := sealDump(w); err != nil {
		return stored, note, err
	}
	return stored, note, nil
}

// storeDump keeps an export unless it is pristine or unchanged (see backupDump)
// and records it as the workload's dump state.
func (m *Manager) storeDump(w *store.Workload, bk backup.Store, file string, man dumpManifest, force bool) (*backup.Backup, string, error) {
	st := &dumpState{At: time.Now().UTC(), ContentHash: man.ContentHash, Pristine: man.Pristine.Pristine}
	var stored *backup.Backup
	note := ""
	prev, _ := readDumpState(w)
	switch {
	case man.Pristine.Pristine && !force:
		note = "pristine: migrations and seed only, nothing to keep"
	case !force && prev != nil && prev.ContentHash == man.ContentHash && prev.BackupID != "" && hasBackup(bk, w.ID, prev.BackupID):
		st.BackupID = prev.BackupID
		note = "unchanged since " + prev.BackupID
	default:
		b, err := bk.Put(w.ID, file, backup.KindDump)
		if err != nil {
			return nil, "", err
		}
		_ = bk.Retain(w.ID, m.cfg.BackupRetain)
		st.BackupID = b.ID
		stored = &b
		m.emit("backup.created", w.ProjectID, w.ID, b.ID)
	}
	if err := writeDumpState(w, st); err != nil {
		return stored, note, err
	}
	return stored, note, nil
}

// dumpBeforeSuspend dumps a dump-backed workload that is about to be suspended
// and returns it, or nil when it is not one or the dump failed (the suspend goes
// ahead either way; the next sweep exports it offline). Caller holds refLock.
func (m *Manager) dumpBeforeSuspend(ctx context.Context, id string) *store.Workload {
	w, err := m.store.GetWorkload(id)
	if err != nil || !m.dumps(w.Type) || !m.BackupsEnabled() {
		return nil
	}
	b, note, err := m.backupDump(ctx, w, false)
	switch {
	case err != nil:
		log.Printf("dump %s before suspend: %v", id, err)
		return nil
	case b != nil:
		log.Printf("dump %s before suspend -> %s (%d bytes)", id, b.ID, b.SizeBytes)
	default:
		log.Printf("dump %s before suspend: %s", id, note)
	}
	return w
}

// errWorkloadGone: a sweep reached a workload that was deleted (evicted) after
// it listed them. There is nothing left to back up.
var errWorkloadGone = errors.New("workload was deleted")

// dailyDump is the scheduled backup of a dump-backed workload: a running
// database is dumped in place, a stopped one is covered by its sealed dump or
// exported offline. An error means it is not covered.
func (m *Manager) dailyDump(ctx context.Context, w *store.Workload) error {
	rl := m.refLock(w.ID)
	rl.Lock()
	defer rl.Unlock()
	// The sweep's list is minutes old by the time it reaches w. An eviction in
	// between removed the data dir, and an offline export of it would boot a new,
	// empty Postgres there instead.
	if _, err := m.store.GetWorkload(w.ID); errors.Is(err, store.ErrNotFound) {
		return errWorkloadGone
	}
	if st, _ := m.rt.Status(ctx, w.ID); st != runtime.StateRunning && dumpCurrent(w) {
		return nil
	}
	b, note, err := m.backupDump(ctx, w, false)
	switch {
	case err != nil:
		log.Printf("dump %s: %v", w.ID, err)
		return err
	case b != nil:
		log.Printf("dump %s -> %s (%d bytes)", w.ID, b.ID, b.SizeBytes)
	default:
		log.Printf("dump %s: %s", w.ID, note)
	}
	return nil
}
