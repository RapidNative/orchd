// Package backup provides byte-exact, restorable snapshots of a workload's data
// volume as tar+gzip archives. The Store interface abstracts where archives live
// so an off-box target (S3/R2) can slot in behind the same API; LocalStore keeps
// them on the box's filesystem.
//
// Archives capture the whole data dir (Postgres cluster + storage objects) taken
// while the instance is stopped, so a restore is the exact bytes back. Callers
// (the manager) coordinate stopping/starting the instance around Create/Restore.
//
// A store also keeps archives made elsewhere (Put/Fetch): a tinbase export
// (`tinbase db export`), a logical dump that is KBs where the data dir is MBs and
// that the workload itself loads on its next boot.
package backup

import (
	"errors"
	"fmt"
	"io"
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
	// Kind is what the archive holds: KindDir or KindDump.
	Kind string `json:"kind,omitempty"`
}

const (
	// KindDir is a tar.gz of the workload's data directory (Create/Restore).
	KindDir = ""
	// KindDump is a tinbase export, stored with Put and read back with Fetch.
	KindDump = "dump"
)

// ErrNotExtractable is returned by Restore for a backup that is not a data-dir
// archive: a tinbase export is loaded by tinbase itself, not extracted.
var ErrNotExtractable = errors.New("backup is a tinbase export: fetch it and let tinbase import it")

// Store is the backup backend contract.
type Store interface {
	Create(workloadID, dataDir string, exclude []string) (Backup, error)
	List(workloadID string) ([]Backup, error) // "" lists every workload's backups
	Restore(id, destDir string) error         // extract into destDir (caller has cleared it)
	Delete(id string) error
	Retain(workloadID string, keep int) error // drop oldest beyond keep
	// Put stores an archive made elsewhere (a tinbase export) as a backup of kind.
	Put(workloadID, file, kind string) (Backup, error)
	// Fetch writes a backup's archive, as stored, to destFile.
	Fetch(id, destFile string) error
}

const tsLayout = "20060102T150405Z"

// LocalStore stores archives at <root>/<workloadID>/<timestamp>[.<kind>].tar.gz.
type LocalStore struct{ root string }

func NewLocalStore(root string) (*LocalStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &LocalStore{root: root}, nil
}

// backupID is <workloadID>__<timestamp>, with .<kind> appended for anything but
// a data-dir archive, so ids minted before kinds existed still parse.
func backupID(workloadID string, t time.Time, kind string) string {
	id := workloadID + "__" + t.UTC().Format(tsLayout)
	if kind != KindDir {
		id += "." + kind
	}
	return id
}

func parseID(id string) (workloadID string, ts time.Time, kind string, err error) {
	i := strings.LastIndex(id, "__")
	if i < 0 {
		return "", time.Time{}, "", fmt.Errorf("bad backup id %q", id)
	}
	workloadID = id[:i]
	rest := id[i+2:]
	if j := strings.IndexByte(rest, '.'); j >= 0 {
		rest, kind = rest[:j], rest[j+1:]
		if kind != KindDump {
			return "", time.Time{}, "", fmt.Errorf("bad backup id %q", id)
		}
	}
	ts, err = time.Parse(tsLayout, rest)
	return
}

// KindOf is the kind of backup an id names; KindDir when it doesn't parse.
func KindOf(id string) string {
	_, _, kind, _ := parseID(id)
	return kind
}

// fileName is the archive's name within its workload's directory or prefix.
func fileName(ts time.Time, kind string) string {
	n := ts.UTC().Format(tsLayout)
	if kind != KindDir {
		n += "." + kind
	}
	return n + ".tar.gz"
}

// parseFileName reads an archive name back; ok is false for anything else.
func parseFileName(name string) (ts time.Time, kind string, ok bool) {
	base := strings.TrimSuffix(name, ".tar.gz")
	if base == name {
		return time.Time{}, "", false
	}
	if j := strings.IndexByte(base, '.'); j >= 0 {
		base, kind = base[:j], base[j+1:]
		if kind != KindDump {
			return time.Time{}, "", false
		}
	}
	ts, err := time.Parse(tsLayout, base)
	return ts, kind, err == nil
}

func (s *LocalStore) path(id string) (string, error) {
	wid, ts, kind, err := parseID(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.root, wid, fileName(ts, kind)), nil
}

func (s *LocalStore) Create(workloadID, dataDir string, exclude []string) (Backup, error) {
	now := time.Now().UTC()
	id := backupID(workloadID, now, KindDir)
	dir := filepath.Join(s.root, workloadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Backup{}, err
	}
	dst := filepath.Join(dir, fileName(now, KindDir))

	// Write to a temp file first, then rename, so a crash never leaves a partial
	// archive that looks valid.
	tmp := dst + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		return Backup{}, err
	}
	if err := writeTarGz(f, dataDir, exclude); err != nil {
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
			if e.IsDir() {
				continue
			}
			ts, kind, ok := parseFileName(e.Name())
			if !ok {
				continue
			}
			fi, _ := e.Info()
			out = append(out, Backup{ID: backupID(wid, ts, kind), WorkloadID: wid, CreatedAt: ts, SizeBytes: sizeOf(fi), Kind: kind})
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
	if _, _, kind, err := parseID(id); err == nil && kind != KindDir {
		return ErrNotExtractable
	}
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

func (s *LocalStore) Put(workloadID, file, kind string) (Backup, error) {
	now := time.Now().UTC()
	dir := filepath.Join(s.root, workloadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Backup{}, err
	}
	dst := filepath.Join(dir, fileName(now, kind))
	if err := copyFile(file, dst+".partial"); err != nil {
		os.Remove(dst + ".partial")
		return Backup{}, err
	}
	if err := os.Rename(dst+".partial", dst); err != nil {
		return Backup{}, err
	}
	fi, _ := os.Stat(dst)
	return Backup{ID: backupID(workloadID, now, kind), WorkloadID: workloadID, CreatedAt: now, SizeBytes: sizeOf(fi), Kind: kind}, nil
}

func (s *LocalStore) Fetch(id, destFile string) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	return copyFile(p, destFile)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func sizeOf(fi os.FileInfo) int64 {
	if fi == nil {
		return 0
	}
	return fi.Size()
}
