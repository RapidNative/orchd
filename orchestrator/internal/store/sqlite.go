package store

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// SQLitePersister stores the whole state snapshot as a single row in a SQLite
// database (WAL mode). It satisfies the blob Persister seam, so nothing above it
// changes: the win over the JSON FilePersister is atomic, transactional,
// crash-safe writes and a real .db file that can be replicated off-box (e.g.
// Litestream, or the built-in control-plane backup) without running a separate
// database server. For a single-writer control plane this is the recommended
// durable backend; Postgres is for a distributed/HA control plane.
type SQLitePersister struct {
	db *sql.DB
}

// OpenSQLite opens (creating if needed) a SQLite-backed store at path. On first
// use it migrates a legacy projects.json sitting next to it, so switching a box
// from the JSON store to SQLite carries the existing index over automatically.
func OpenSQLite(path string) (Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	p, err := newSQLitePersister(path)
	if err != nil {
		return nil, err
	}
	// One-time migration from the legacy JSON file, if present and the DB is empty.
	if existing, _ := p.Load(); len(existing) == 0 {
		legacy := filepath.Join(filepath.Dir(path), "projects.json")
		if b, err := os.ReadFile(legacy); err == nil && len(b) > 0 {
			if err := p.Save(b); err != nil {
				return nil, err
			}
		}
	}
	return openWith(p)
}

func newSQLitePersister(path string) (*SQLitePersister, error) {
	// WAL for durable incremental writes; busy_timeout so a concurrent reader
	// (a backup checkpoint) never returns SQLITE_BUSY.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer keeps WAL semantics simple
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		snapshot BLOB NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLitePersister{db: db}, nil
}

func (p *SQLitePersister) Load() ([]byte, error) {
	var b []byte
	err := p.db.QueryRow(`SELECT snapshot FROM state WHERE id = 1`).Scan(&b)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

func (p *SQLitePersister) Save(b []byte) error {
	_, err := p.db.Exec(
		`INSERT INTO state (id, snapshot) VALUES (1, ?)
		 ON CONFLICT(id) DO UPDATE SET snapshot = excluded.snapshot`, b)
	return err
}

// Checkpoint flushes the WAL into the main .db file so a file-level copy of the
// state directory is a complete, consistent snapshot. Called before the
// control-plane state is backed up off-box.
func (p *SQLitePersister) Checkpoint() error {
	_, err := p.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}
