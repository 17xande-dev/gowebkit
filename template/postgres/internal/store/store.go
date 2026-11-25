// Package store persists application accounts and sessions.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/db"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/db/gen"
	"github.com/17xande-dev/password"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound           = errors.New("record not found")
	ErrEmailInUse         = errors.New("email is already in use")
	ErrLastAdmin          = errors.New("at least one enabled admin is required")
	ErrSetupUnavailable   = errors.New("setup is unavailable")
	ErrCredentialsChanged = errors.New("account credentials changed")
)

// Store uses the migrated database without owning its lifetime.
type Store struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// New binds a store to the supplied database pool.
func New(d *db.DB) *Store { return &Store{pool: d.Pool(), q: gen.New(d.Pool())} }

// Every timestamp is written and judged using Go's clock, never the server's,
// so the same instant is used for a predicate and the value it protects.
func timestamp(t time.Time) time.Time { return t.UTC() }

func found(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// transact runs fn in a READ COMMITTED transaction. Guards that count rows
// take row locks first (see lockAdmins); the isolation level alone does not
// make a count-then-write safe.
func (s *Store) transact(ctx context.Context, fn func(*gen.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	// A rollback after commit is a no-op; after a failure it releases the locks.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func prepareUser(u *auth.User) error {
	email, err := auth.NormalizeEmail(u.Email)
	if err != nil {
		return err
	}
	u.Email = email
	if err := password.Validate(u.PasswordHash); err != nil {
		return fmt.Errorf("invalid password hash: %w", err)
	}
	return nil
}
