package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/auth"
	"github.com/17xande-dev/password"
)

const userColumns = `id, name, email, role, password_hash, disabled, must_change_password`

func userFields(u *auth.User) []any {
	return []any{&u.ID, &u.Name, &u.Email, &u.Role, &u.PasswordHash, &u.Disabled, &u.MustChangePassword}
}

func scanUser(row interface{ Scan(...any) error }) (auth.User, error) {
	var u auth.User
	if err := row.Scan(userFields(&u)...); err != nil {
		return auth.User{}, found(err)
	}
	return u, nil
}

// UserByEmail looks up an account using its normalized email address.
func (s *Store) UserByEmail(ctx context.Context, email string) (auth.User, error) {
	email, err := auth.NormalizeEmail(email)
	if err != nil {
		return auth.User{}, err
	}
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = ?`, email))
}

// UserByID looks up an account by its stable identifier. Only tests call it
// today; it is part of the store contract, so do not remove it as unused.
func (s *Store) UserByID(ctx context.Context, id int64) (auth.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// UserCount counts all accounts, including disabled accounts.
func (s *Store) UserCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// EnabledAdminCount is for display; writes enforce the last-admin guard themselves.
func (s *Store) EnabledAdminCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0`).Scan(&n)
	return n, err
}

// ListUsers returns accounts in ascending ID order.
func (s *Store) ListUsers(ctx context.Context) ([]auth.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []auth.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// CreateUser returns ErrEmailInUse rather than overwriting an existing account.
func (s *Store) CreateUser(ctx context.Context, u auth.User) (auth.User, error) {
	if err := prepareUser(&u); err != nil {
		return auth.User{}, err
	}
	created, err := insertUser(ctx, s.db, u)
	if errors.Is(err, ErrNotFound) {
		return auth.User{}, ErrEmailInUse
	}
	return created, err
}

func insertUser(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, u auth.User) (auth.User, error) {
	return scanUser(q.QueryRowContext(ctx, `INSERT INTO users
		(name, email, role, password_hash, disabled, must_change_password)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(email) DO NOTHING RETURNING `+userColumns,
		u.Name, u.Email, u.Role, u.PasswordHash, u.Disabled, u.MustChangePassword))
}

// SetRole keeps at least one enabled admin even under concurrent changes.
func (s *Store) SetRole(ctx context.Context, id int64, role auth.Role) error {
	return s.transact(ctx, func(tx *sql.Tx) error {
		ok, err := changed(tx.ExecContext(ctx, `UPDATE users SET role = ?1 WHERE id = ?2
			AND (role <> 'admin' OR disabled = 1 OR ?1 = 'admin'
				OR (SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0) > 1)`, role, id))
		if err != nil || ok {
			return err
		}
		return refusedUserChange(ctx, tx, id)
	})
}

// SetDisabled revokes all sessions in the same transaction that disables a user.
func (s *Store) SetDisabled(ctx context.Context, id int64, disabled bool) error {
	return s.transact(ctx, func(tx *sql.Tx) error {
		ok, err := changed(tx.ExecContext(ctx, `UPDATE users SET disabled = ?1 WHERE id = ?2
			AND (?1 = 0 OR role <> 'admin' OR disabled = 1
				OR (SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0) > 1)`, disabled, id))
		if err != nil {
			return err
		}
		if !ok {
			return refusedUserChange(ctx, tx, id)
		}
		if !disabled {
			return nil
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
		return err
	})
}

func refusedUserChange(ctx context.Context, tx *sql.Tx, id int64) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = ?)`, id).Scan(&exists); err != nil {
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
	return changed(s.db.ExecContext(ctx, `UPDATE users SET password_hash = ?
		WHERE id = ? AND password_hash = ? AND disabled = 0`, newHash, id, oldHash))
}

// ChangePassword replaces a verified password, clears the change requirement,
// and revokes the user's other sessions atomically.
func (s *Store) ChangePassword(ctx context.Context, id int64, oldHash, newHash string, current auth.TokenHash) error {
	if err := password.Validate(newHash); err != nil {
		return err
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		ok, err := changed(tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, must_change_password = 0
			WHERE id = ? AND password_hash = ? AND disabled = 0`, newHash, id, oldHash))
		if err != nil {
			return err
		}
		if !ok {
			return ErrCredentialsChanged
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, id, encodeHash(current))
		return err
	})
}

// ResetPassword installs a temporary password and revokes all the user's sessions.
func (s *Store) ResetPassword(ctx context.Context, id int64, newHash string) error {
	if err := password.Validate(newHash); err != nil {
		return err
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		ok, err := changed(tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, must_change_password = 1 WHERE id = ?`, newHash, id))
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
		return err
	})
}
