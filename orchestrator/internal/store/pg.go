package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// PGPersister stores the whole state snapshot as a single row in Postgres. It is
// the DB adaptor behind Store — the seam for moving the control-plane index off a
// single box (a stepping stone toward a fully relational, multi-writer store).
type PGPersister struct{ dsn string }

func pgCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// NewPGPersister connects, ensures the state table, and returns the persister.
func NewPGPersister(dsn string) (*PGPersister, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS orchd_state (id int PRIMARY KEY, data text NOT NULL, updated_at timestamptz NOT NULL DEFAULT now())`)
	if err != nil {
		return nil, err
	}
	return &PGPersister{dsn: dsn}, nil
}

func (p *PGPersister) Load() ([]byte, error) {
	ctx, cancel := pgCtx()
	defer cancel()
	conn, err := pgx.Connect(ctx, p.dsn)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	var data string
	err = conn.QueryRow(ctx, `SELECT data FROM orchd_state WHERE id = 1`).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(data), nil
}

func (p *PGPersister) Save(b []byte) error {
	ctx, cancel := pgCtx()
	defer cancel()
	conn, err := pgx.Connect(ctx, p.dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx,
		`INSERT INTO orchd_state (id, data, updated_at) VALUES (1, $1, now())
		 ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`, string(b))
	return err
}

// OpenPostgres returns a Postgres-backed store.
func OpenPostgres(dsn string) (Store, error) {
	p, err := NewPGPersister(dsn)
	if err != nil {
		return nil, err
	}
	return openWith(p)
}
