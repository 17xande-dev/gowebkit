package store

import (
	"context"
	"fmt"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/auth"
	"github.com/17xande-dev/websec/session"
)

// CreateSession admits only an enabled user whose verified password hash is
// still current. A password reset or disable cannot race session revocation.
func (s *Store) CreateSession(ctx context.Context, userID int64, passwordHash string, hash auth.TokenHash, now time.Time) error {
	ok, err := changed(s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, created_at, last_seen)
		SELECT ?1, id, ?2, ?2 FROM users WHERE id = ?3 AND password_hash = ?4 AND disabled = 0`,
		encodeHash(hash), timestamp(now), userID, passwordHash))
	if err != nil {
		return err
	}
	if !ok {
		return ErrCredentialsChanged
	}
	return nil
}

// LookupSession returns a session and its enabled user. Expiry is decided by
// the caller using the same Go clock that is passed to the store on writes.
func (s *Store) LookupSession(ctx context.Context, hash auth.TokenHash) (auth.User, auth.Session, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+`, sessions.created_at, sessions.last_seen
		FROM sessions JOIN users ON users.id = sessions.user_id
		WHERE sessions.token_hash = ? AND users.disabled = 0`, encodeHash(hash))
	var u auth.User
	var created, seen string
	if err := row.Scan(append(userFields(&u), &created, &seen)...); err != nil {
		return auth.User{}, auth.Session{}, found(err)
	}
	createdAt, err := time.Parse(timeLayout, created)
	if err != nil {
		return auth.User{}, auth.Session{}, fmt.Errorf("parse session creation time: %w", err)
	}
	lastSeen, err := time.Parse(timeLayout, seen)
	if err != nil {
		return auth.User{}, auth.Session{}, fmt.Errorf("parse session activity time: %w", err)
	}
	return u, auth.Session{UserID: u.ID, CreatedAt: createdAt, LastSeen: lastSeen}, nil
}

// TouchSession throttles concurrent touches and corrects activity from a clock
// that was ahead. The same now is used for the comparison and the new value.
func (s *Store) TouchSession(ctx context.Context, hash auth.TokenHash, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen = ?1
		WHERE token_hash = ?2 AND (last_seen <= ?3 OR last_seen > ?1)`,
		timestamp(now), encodeHash(hash), timestamp(now.Add(-session.TouchEvery)))
	return err
}

// DeleteSession revokes one session, succeeding if it is already absent.
func (s *Store) DeleteSession(ctx context.Context, hash auth.TokenHash) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, encodeHash(hash))
	return err
}

// RevokeSessions ends every session for the given user. Only tests call it today;
// it is part of the store contract (the password and disable paths do the same
// inside their own transactions), so do not remove it as unused.
func (s *Store) RevokeSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// SweepSessions removes rows at either deadline. Idle time can't start before
// creation, including after a clock step back.
func (s *Store) SweepSessions(ctx context.Context, life session.Lifetime, now time.Time) (int64, error) {
	if err := life.Validate(); err != nil {
		return 0, err
	}
	if life.IdleOff {
		return affected(s.db.ExecContext(ctx, `DELETE FROM sessions WHERE created_at <= ?`, timestamp(now.Add(-life.Absolute))))
	}
	return affected(s.db.ExecContext(ctx, `DELETE FROM sessions WHERE created_at <= ?
		OR max(created_at, last_seen) <= ?`, timestamp(now.Add(-life.Absolute)), timestamp(now.Add(-life.Idle))))
}
