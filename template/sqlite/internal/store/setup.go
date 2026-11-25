package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/auth"
)

// SetupState returns the token digest and whether it has been consumed.
func (s *Store) SetupState(ctx context.Context) (auth.SetupState, error) {
	var state auth.SetupState
	var hash string
	if err := s.db.QueryRowContext(ctx, `SELECT token_hash, consumed FROM setup WHERE id = 1`).
		Scan(&hash, &state.Consumed); err != nil {
		return auth.SetupState{}, found(err)
	}
	if n, err := hex.Decode(state.TokenHash[:], []byte(hash)); err != nil || n != len(state.TokenHash) {
		return auth.SetupState{}, errors.New("invalid stored setup token digest")
	}
	return state, nil
}

// IssueSetupToken installs the first digest if no account or token exists.
func (s *Store) IssueSetupToken(ctx context.Context, hash auth.TokenHash) (bool, error) {
	return changed(s.db.ExecContext(ctx, `INSERT INTO setup (id, token_hash)
		SELECT 1, ? WHERE NOT EXISTS (SELECT 1 FROM users) ON CONFLICT(id) DO NOTHING`, encodeHash(hash)))
}

// ClaimSetup creates the first admin and spends the token, or does neither.
func (s *Store) ClaimSetup(ctx context.Context, hash auth.TokenHash, u auth.User) (auth.User, error) {
	if err := prepareUser(&u); err != nil {
		return auth.User{}, err
	}
	u.Role, u.Disabled, u.MustChangePassword = auth.RoleAdmin, false, false
	var created auth.User
	err := s.transact(ctx, func(tx *sql.Tx) error {
		ok, err := changed(tx.ExecContext(ctx, `UPDATE setup SET consumed = 1
			WHERE id = 1 AND token_hash = ? AND consumed = 0 AND NOT EXISTS (SELECT 1 FROM users)`, encodeHash(hash)))
		if err != nil {
			return err
		}
		if !ok {
			return ErrSetupUnavailable
		}
		created, err = insertUser(ctx, tx, u)
		return err
	})
	if err != nil {
		return auth.User{}, err
	}
	return created, nil
}
