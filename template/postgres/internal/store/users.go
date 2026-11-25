package store

import (
	"context"
	"errors"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/db/gen"
	"github.com/17xande-dev/password"
)

func fromRow(u gen.User) auth.User {
	return auth.User{
		ID: u.ID, Name: u.Name, Email: u.Email, Role: auth.Role(u.Role), PasswordHash: u.PasswordHash,
		Disabled: u.Disabled, MustChangePassword: u.MustChangePassword,
	}
}

// UserByEmail looks up an account using its normalized email address.
func (s *Store) UserByEmail(ctx context.Context, email string) (auth.User, error) {
	email, err := auth.NormalizeEmail(email)
	if err != nil {
		return auth.User{}, err
	}
	u, err := s.q.UserByEmail(ctx, email)
	if err != nil {
		return auth.User{}, found(err)
	}
	return fromRow(u), nil
}

// UserByID looks up an account by its stable identifier. Only tests call it
// today; it is part of the store contract, so do not remove it as unused.
func (s *Store) UserByID(ctx context.Context, id int64) (auth.User, error) {
	u, err := s.q.UserByID(ctx, id)
	if err != nil {
		return auth.User{}, found(err)
	}
	return fromRow(u), nil
}

// UserCount counts all accounts, including disabled accounts.
func (s *Store) UserCount(ctx context.Context) (int64, error) { return s.q.UserCount(ctx) }

// EnabledAdminCount is for display; writes enforce the last-admin guard themselves.
func (s *Store) EnabledAdminCount(ctx context.Context) (int64, error) {
	return s.q.EnabledAdminCount(ctx)
}

// ListUsers returns accounts in ascending ID order.
func (s *Store) ListUsers(ctx context.Context) ([]auth.User, error) {
	rows, err := s.q.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	users := make([]auth.User, 0, len(rows))
	for _, u := range rows {
		users = append(users, fromRow(u))
	}
	return users, nil
}

// CreateUser returns ErrEmailInUse rather than overwriting an existing account.
func (s *Store) CreateUser(ctx context.Context, u auth.User) (auth.User, error) {
	if err := prepareUser(&u); err != nil {
		return auth.User{}, err
	}
	created, err := insertUser(ctx, s.q, u)
	if errors.Is(err, ErrNotFound) {
		return auth.User{}, ErrEmailInUse
	}
	return created, err
}

func insertUser(ctx context.Context, q *gen.Queries, u auth.User) (auth.User, error) {
	row, err := q.InsertUser(ctx, gen.InsertUserParams{
		Name: u.Name, Email: u.Email, Role: string(u.Role), PasswordHash: u.PasswordHash,
		Disabled: u.Disabled, MustChangePassword: u.MustChangePassword,
	})
	if err != nil {
		return auth.User{}, found(err)
	}
	return fromRow(row), nil
}

// lockAdmins takes row locks on every enabled admin before a change that could
// remove the last one. READ COMMITTED does not make "count the admins, then write" safe:
// two concurrent changes each count two and together remove both. A change
// that holds these locks is alone in deciding, and the guarded statement that
// follows counts after any earlier change has committed.
func lockAdmins(ctx context.Context, q *gen.Queries) error {
	_, err := q.LockEnabledAdmins(ctx)
	return err
}

// SetRole keeps at least one enabled admin even under concurrent changes.
func (s *Store) SetRole(ctx context.Context, id int64, role auth.Role) error {
	return s.transact(ctx, func(q *gen.Queries) error {
		if err := lockAdmins(ctx, q); err != nil {
			return err
		}
		n, err := q.SetRole(ctx, gen.SetRoleParams{NewRole: string(role), UserID: id})
		if err != nil || n != 0 {
			return err
		}
		return refusedUserChange(ctx, q, id)
	})
}

// SetDisabled revokes all sessions in the same transaction that disables a user.
func (s *Store) SetDisabled(ctx context.Context, id int64, disabled bool) error {
	return s.transact(ctx, func(q *gen.Queries) error {
		if err := lockAdmins(ctx, q); err != nil {
			return err
		}
		n, err := q.SetDisabled(ctx, gen.SetDisabledParams{Disabled: disabled, UserID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return refusedUserChange(ctx, q, id)
		}
		if !disabled {
			return nil
		}
		return q.DeleteUserSessions(ctx, id)
	})
}

func refusedUserChange(ctx context.Context, q *gen.Queries, id int64) error {
	exists, err := q.UserExists(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return ErrLastAdmin
}

// RehashPassword saves a format upgrade only if the verified hash is current.
func (s *Store) RehashPassword(ctx context.Context, id int64, oldHash, newHash string) (bool, error) {
	if err := password.Validate(newHash); err != nil {
		return false, err
	}
	n, err := s.q.RehashPassword(ctx, gen.RehashPasswordParams{NewHash: newHash, UserID: id, OldHash: oldHash})
	return n != 0, err
}

// ChangePassword replaces a verified password, clears the change requirement,
// and revokes the user's other sessions atomically. The user row stays locked
// until commit, so a sign-in that read the old password waits and then fails.
func (s *Store) ChangePassword(ctx context.Context, id int64, oldHash, newHash string, current auth.TokenHash) error {
	if err := password.Validate(newHash); err != nil {
		return err
	}
	return s.transact(ctx, func(q *gen.Queries) error {
		n, err := q.ChangePassword(ctx, gen.ChangePasswordParams{NewHash: newHash, UserID: id, OldHash: oldHash})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrCredentialsChanged
		}
		return q.DeleteOtherUserSessions(ctx, gen.DeleteOtherUserSessionsParams{UserID: id, TokenHash: current[:]})
	})
}

// ResetPassword installs a temporary password and revokes all the user's sessions.
func (s *Store) ResetPassword(ctx context.Context, id int64, newHash string) error {
	if err := password.Validate(newHash); err != nil {
		return err
	}
	return s.transact(ctx, func(q *gen.Queries) error {
		n, err := q.ResetPassword(ctx, gen.ResetPasswordParams{PasswordHash: newHash, ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return q.DeleteUserSessions(ctx, id)
	})
}
