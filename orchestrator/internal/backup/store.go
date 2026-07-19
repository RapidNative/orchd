// Package backup provides byte-exact, restorable snapshots of a workload's data
// volume as tar+gzip archives. The Store interface abstracts where archives live
// so an off-box target (S3/R2) can slot in behind the same API; LocalStore keeps
// them on the box's filesystem.
//
// Archives capture the whole data dir (Postgres cluster + storage objects) taken
// while the instance is stopped, so a restore is the exact bytes back. Callers
// (the manager) coordinate stopping/starting the instance around Create/Restore.
package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backup is a single stored snapshot.
type Backup struct {
	ID         string    `json:"id"`
	WorkloadID string    `json:"workload_id"`
	CreatedAt  time.Time `json:"created_at"`
	SizeBytes  int64     `json:"size_bytes"`
}

// Store is the backup backend contract.
type Store interface {
	Create(workloadID, dataDir string) (Backup, error)
	List(workloadID string) ([]Backup, error) // "" lists every workload's backups
	Restore(id, destDir string) error         // extract into destDir (caller has cleared it)
	Delete(id string) error
	Retain(workloadID string, keep int) error // drop oldest beyond keep
}

const tsLayout = "20060102T150405Z"

// LocalStore stores archives at <root>/<workloadID>/<timestamp>.tar.gz.
type LocalStore struct{ root string }

func NewLocalStore(root string) (*LocalStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &LocalStore{root: root}, nil
}

func backupID(workloadID string, t time.Time) string {
	return workloadID + "__" + t.UTC().Format(tsLayout)
}

func parseID(id string) (workloadID string, ts time.Time, err error) {
	i := strings.LastIndex(id, "__")
	if i < 0 {
		return "", time.Time{}, fmt.Errorf("bad backup id %q", id)
	}
	workloadID = id[:i]
	ts, err = time.Parse(tsLayout, id[i+2:])
	return
}

func (s *LocalStore) path(id string) (string, error) {
	wid, ts, err := parseID(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.root, wid, ts.UTC().Format(tsLayout)+".tar.gz"), nil
}

func (s *LocalStore) Create(workloadID, dataDir string) (Backup, error) {
	now := time.Now().UTC()
	id := backupID(workloadID, now)
	dir := filepath.Join(s.root, workloadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Backup{}, err
	}
	dst := filepath.Join(dir, now.Format(tsLayout)+".tar.gz")

	// Write to a temp file first, then rename, so a crash never leaves a partial
	// archive that looks valid.
	tmp := dst + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return Backup{}, err
	}
	if err := writeTarGz(f, dataDir); err != nil {
		f.Close()
		os.Remove(tmp)
		return Backup{}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return Backup{}, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return Backup{}, err
	}
	fi, _ := os.Stat(dst)
	return Backup{ID: id, WorkloadID: workloadID, CreatedAt: now, SizeBytes: sizeOf(fi)}, nil
}

func (s *LocalStore) List(workloadID string) ([]Backup, error) {
	var out []Backup
	walkDir := func(wid string) {
		entries, err := os.ReadDir(filepath.Join(s.root, wid))
		if err != nil {
			return
		}
		for _, e := range entries {
			name := strings.TrimSuffix(e.Name(), ".tar.gz")
			if name == e.Name() || e.IsDir() {
				continue
			}
			ts, err := time.Parse(tsLayout, name)
			if err != nil {
				continue
			}
			fi, _ := e.Info()
			out = append(out, Backup{ID: backupID(wid, ts), WorkloadID: wid, CreatedAt: ts, SizeBytes: sizeOf(fi)})
		}
	}
	if workloadID != "" {
		walkDir(workloadID)
	} else {
		entries, _ := os.ReadDir(s.root)
		for _, e := range entries {
			if e.IsDir() {
				walkDir(e.Name())
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *LocalStore) Restore(id, destDir string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer f.Close()
	return extractTarGz(f, destDir)
}

func (s *LocalStore) Delete(id string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *LocalStore) Retain(workloadID string, keep int) error {
	list, err := s.List(workloadID)
	if err != nil {
		return err
	}
	// list is newest-first; delete everything past `keep`.
	for i := keep; i < len(list); i++ {
		_ = s.Delete(list[i].ID)
	}
	return nil
}

func sizeOf(fi os.FileInfo) int64 {
	if fi == nil {
		return 0
	}
	return fi.Size()
}
