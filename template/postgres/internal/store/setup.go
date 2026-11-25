package store

import (
	"context"
	"errors"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/db/gen"
)

// SetupState returns the token digest and whether it has been consumed. A
// stored digest of the wrong length is refused before anything compares it.
func (s *Store) SetupState(ctx context.Context) (auth.SetupState, error) {
	row, err := s.q.SetupState(ctx)
	if err != nil {
		return auth.SetupState{}, found(err)
	}
	var state auth.SetupState
	if len(row.TokenHash) != len(state.TokenHash) {
		return auth.SetupState{}, errors.New("invalid stored setup token digest")
	}
	copy(state.TokenHash[:], row.TokenHash)
	state.Consumed = row.Consumed
	return state, nil
}

// IssueSetupToken installs the first digest if no account or token exists.
func (s *Store) IssueSetupToken(ctx context.Context, hash auth.TokenHash) (bool, error) {
	n, err := s.q.IssueSetupToken(ctx, hash[:])
	return n != 0, err
}

// ClaimSetup creates the first admin and spends the token, or does neither.
func (s *Store) ClaimSetup(ctx context.Context, hash auth.TokenHash, u auth.User) (auth.User, error) {
	if err := prepareUser(&u); err != nil {
		return auth.User{}, err
	}
	u.Role, u.Disabled, u.MustChangePassword = auth.RoleAdmin, false, false
	var created auth.User
	err := s.transact(ctx, func(q *gen.Queries) error {
		n, err := q.ConsumeSetupToken(ctx, hash[:])
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrSetupUnavailable
		}
		created, err = insertUser(ctx, q, u)
		return err
	})
	if err != nil {
		return auth.User{}, err
	}
	return created, nil
}
