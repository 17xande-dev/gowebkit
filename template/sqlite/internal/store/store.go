// Package store persists application accounts and sessions.
package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/auth"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/db"
	"github.com/17xande-dev/password"
)

var (
	ErrNotFound           = errors.New("record not found")
	ErrEmailInUse         = errors.New("email is already in use")
	ErrLastAdmin          = errors.New("at least one enabled admin is required")
	ErrSetupUnavailable   = errors.New("setup is unavailable")
	ErrCredentialsChanged = errors.New("account credentials changed")
)

// Store uses the migrated database without owning its lifetime.
type Store struct{ db *db.DB }

// New binds a store to the supplied database pool.
func New(d *db.DB) *Store { return &Store{db: d} }

// Fixed-width UTC text sorts by time, including across whole-second boundaries.
const timeLayout = "2006-01-02T15:04:05.000000Z"

func timestamp(t time.Time) string { return t.UTC().Format(timeLayout) }

func encodeHash(h auth.TokenHash) string { return hex.EncodeToString(h[:]) }

func found(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func affected(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func changed(res sql.Result, err error) (bool, error) {
	n, err := affected(res, err)
	return n != 0, err
}

// The driver's immediate transactions serialize a guard and its dependent
// writes, without exposing a transaction to handlers.
func (s *Store) transact(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
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
