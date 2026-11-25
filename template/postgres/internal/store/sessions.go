package store

import (
	"context"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/db/gen"
	"github.com/17xande-dev/websec/session"
)

// CreateSession admits only an enabled user whose verified password hash is
// still current. The insert locks the user row for share, so a concurrent
// password reset or disable that has not committed makes it wait, then fail,
// instead of leaving a session that outlives the revocation.
func (s *Store) CreateSession(ctx context.Context, userID int64, passwordHash string, hash auth.TokenHash, now time.Time) error {
	n, err := s.q.CreateSession(ctx, gen.CreateSessionParams{
		TokenHash: hash[:], Now: timestamp(now), UserID: userID, PasswordHash: passwordHash,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrCredentialsChanged
	}
	return nil
}

// LookupSession returns a session and its enabled user. Expiry is decided by
// the caller using the same Go clock that is passed to the store on writes.
func (s *Store) LookupSession(ctx context.Context, hash auth.TokenHash) (auth.User, auth.Session, error) {
	row, err := s.q.LookupSession(ctx, hash[:])
	if err != nil {
		return auth.User{}, auth.Session{}, found(err)
	}
	u := fromRow(gen.User{
		ID: row.ID, Name: row.Name, Email: row.Email, Role: row.Role, PasswordHash: row.PasswordHash,
		Disabled: row.Disabled, MustChangePassword: row.MustChangePassword,
	})
	return u, auth.Session{UserID: u.ID, CreatedAt: row.CreatedAt.UTC(), LastSeen: row.LastSeen.UTC()}, nil
}

// TouchSession throttles concurrent touches and corrects activity from a clock
// that was ahead. The same now is used for the comparison and the new value.
func (s *Store) TouchSession(ctx context.Context, hash auth.TokenHash, now time.Time) error {
	return s.q.TouchSession(ctx, gen.TouchSessionParams{
		Now: timestamp(now), TokenHash: hash[:], TouchedBefore: timestamp(now.Add(-session.TouchEvery)),
	})
}

// DeleteSession revokes one session, succeeding if it is already absent.
func (s *Store) DeleteSession(ctx context.Context, hash auth.TokenHash) error {
	return s.q.DeleteSession(ctx, hash[:])
}

// RevokeSessions ends every session for the given user. Only tests call it today;
// it is part of the store contract (the password and disable paths do the same
// inside their own transactions), so do not remove it as unused.
func (s *Store) RevokeSessions(ctx context.Context, userID int64) error {
	return s.q.DeleteUserSessions(ctx, userID)
}

// SweepSessions removes rows at either deadline. Idle time can't start before
// creation, including after a clock step back.
func (s *Store) SweepSessions(ctx context.Context, life session.Lifetime, now time.Time) (int64, error) {
	if err := life.Validate(); err != nil {
		return 0, err
	}
	if life.IdleOff {
		return s.q.SweepAbsolute(ctx, timestamp(now.Add(-life.Absolute)))
	}
	return s.q.SweepAbsoluteOrIdle(ctx, gen.SweepAbsoluteOrIdleParams{
		AbsoluteBefore: timestamp(now.Add(-life.Absolute)), IdleBefore: timestamp(now.Add(-life.Idle)),
	})
}
