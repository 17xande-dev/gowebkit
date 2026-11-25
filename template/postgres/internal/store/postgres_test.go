package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/dbtest"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
)

var fixedNow = time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)

// Under READ COMMITTED two concurrent changes can each count two enabled
// admins and together remove them all, so a change locks the admin rows first. The
// test plays the other change by holding those locks in a transaction, starts
// the real store call, and requires it to wait; it then commits a change that
// leaves one admin, and the call that waited must see that and refuse.
func TestPostgresLastAdminGuardWaitsForAdminRowLocks(t *testing.T) {
	for _, tt := range []struct {
		name string
		call func(*store.Store, int64) error
	}{
		{"disable", func(s *store.Store, id int64) error { return s.SetDisabled(t.Context(), id, true) }},
		{"demote", func(s *store.Store, id int64) error { return s.SetRole(t.Context(), id, auth.RoleMember) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, d := contractStore(t)
			first := createUser(t, s, "first-admin@example.com", auth.RoleAdmin)
			second := createUser(t, s, "second-admin@example.com", auth.RoleAdmin)

			holder, err := d.Pool().Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback(t.Context())
			if _, err := holder.Exec(t.Context(), `SELECT id FROM users WHERE role = 'admin' AND NOT disabled FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- tt.call(s, second.ID) }()
			dbtest.WaitUntilBlocked(t, d, "FOR UPDATE", 1, done)

			// The holder's change commits while the call waits.
			if _, err := holder.Exec(t.Context(), `UPDATE users SET disabled = true WHERE id = $1`, first.ID); err != nil {
				t.Fatal(err)
			}
			if err := holder.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			requireErr(t, <-done, store.ErrLastAdmin)
			if n, err := s.EnabledAdminCount(t.Context()); err != nil || n != 1 {
				t.Fatalf("enabled admins = %d, %v; the waiting change must have been refused", n, err)
			}
			got, err := s.UserByID(t.Context(), second.ID)
			if err != nil || got.Disabled || got.Role != auth.RoleAdmin {
				t.Fatalf("the refused change altered the admin: %+v, %v", got, err)
			}
		})
	}
}

// The guard must lock ALL enabled admins, not only the row it changes: two
// concurrent changes to two different admins would otherwise each lock their
// own row, each count two, and together remove both. Here the other change
// holds only the OTHER admin's row, so a change that locked only its target
// would not wait.
func TestPostgresLastAdminGuardLocksEveryEnabledAdmin(t *testing.T) {
	for _, tt := range []struct {
		name string
		call func(*store.Store, int64) error
	}{
		{"disable", func(s *store.Store, id int64) error { return s.SetDisabled(t.Context(), id, true) }},
		{"demote", func(s *store.Store, id int64) error { return s.SetRole(t.Context(), id, auth.RoleMember) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, d := contractStore(t)
			first := createUser(t, s, "first-admin@example.com", auth.RoleAdmin)
			second := createUser(t, s, "second-admin@example.com", auth.RoleAdmin)

			holder, err := d.Pool().Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback(t.Context())
			if _, err := holder.Exec(t.Context(), `SELECT id FROM users WHERE id = $1 FOR UPDATE`, first.ID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- tt.call(s, second.ID) }()
			dbtest.WaitUntilBlocked(t, d, "FOR UPDATE", 1, done)

			// Releasing the other admin's row lets the change through: two
			// admins remained enabled when it counted.
			if err := holder.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("the change should proceed once the other admin is released: %v", err)
			}
			if n, err := s.EnabledAdminCount(t.Context()); err != nil || n != 1 {
				t.Fatalf("enabled admins = %d, %v", n, err)
			}
		})
	}
}

// A sign-in that read the old password must not create a session when a
// reset, a password change or a disable commits first. The insert locks the
// user row for share, so it waits for that transaction and then re-checks.
func TestPostgresCreateSessionWaitsForConcurrentUserChange(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change string
	}{
		{"reset", `UPDATE users SET password_hash = 'replacement', must_change_password = true WHERE id = $1`},
		{"disable", `UPDATE users SET disabled = true WHERE id = $1`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, d := contractStore(t)
			user := createUser(t, s, "member-account@example.com", auth.RoleMember)

			change, err := d.Pool().Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer change.Rollback(t.Context())
			if _, err := change.Exec(t.Context(), tt.change, user.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := change.Exec(t.Context(), `DELETE FROM sessions WHERE user_id = $1`, user.ID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- s.CreateSession(t.Context(), user.ID, user.PasswordHash, auth.HashToken(auth.NewToken()), fixedNow)
			}()
			dbtest.WaitUntilBlocked(t, d, "FOR SHARE", 1, done)
			if err := change.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			requireErr(t, <-done, store.ErrCredentialsChanged)
			if rows := rawRows(t, d); len(rows) != 0 {
				t.Fatalf("a session survived the change: %v", rows)
			}
		})
	}
}

// The setup digest is compared in constant time as a 32-byte array. A stored
// value of any other length is refused before that, not zero-padded or cut.
func TestPostgresSetupStateRefusesAStoredDigestOfTheWrongLength(t *testing.T) {
	s, d := contractStore(t)
	if ok, err := s.IssueSetupToken(t.Context(), auth.HashToken("setup-token-sample")); !ok || err != nil {
		t.Fatalf("issue: %v, %v", ok, err)
	}
	// The column's CHECK keeps this from happening; remove it to model a
	// database that was edited by hand.
	if _, err := d.ExecContext(t.Context(), `ALTER TABLE setup DROP CONSTRAINT setup_token_hash_check`); err != nil {
		t.Fatal(err)
	}
	for _, stored := range []string{`decode('00', 'hex')`, `decode(repeat('00', 33), 'hex')`} {
		if _, err := d.ExecContext(t.Context(), `UPDATE setup SET token_hash = `+stored); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetupState(t.Context()); err == nil || errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a %s digest was accepted: %v", stored, err)
		}
	}
}
