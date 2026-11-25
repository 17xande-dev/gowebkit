package store_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/auth"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/db"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/dbtest"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/store"
	"github.com/17xande-dev/password"
	"github.com/17xande-dev/websec/session"
)

func TestMain(m *testing.M) {
	p := password.DefaultParams
	p.Memory, p.Time = 128, 1
	defer password.SetParamsForTesting(p)()
	m.Run()
}

func contractStore(t *testing.T) (*store.Store, *db.DB) {
	t.Helper()
	d := dbtest.Open(t)
	return store.New(d), d
}

func fixtureHash(t *testing.T) string {
	t.Helper()
	hash, err := auth.HashPassword(t.Context(), "fixture password")
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func createUser(t *testing.T, s *store.Store, email string, role auth.Role) auth.User {
	t.Helper()
	u, err := s.CreateUser(t.Context(), auth.User{
		Name: "Sample", Email: email, Role: role, PasswordHash: fixtureHash(t), MustChangePassword: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func createSession(t *testing.T, s *store.Store, user auth.User, now time.Time) string {
	t.Helper()
	token := auth.NewToken()
	if err := s.CreateSession(t.Context(), user.ID, user.PasswordHash, auth.HashToken(token), now); err != nil {
		t.Fatal(err)
	}
	return token
}

func requireErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}

func TestStoreUserIdentity(t *testing.T) {
	s, _ := contractStore(t)
	ctx := t.Context()
	u := createUser(t, s, " Sample <Account@Example.com> ", auth.RoleMember)
	if u.ID <= 0 || u.Email != "account@example.com" || !u.MustChangePassword {
		t.Fatalf("invalid persisted user: id=%d email=%q must change=%v", u.ID, u.Email, u.MustChangePassword)
	}
	if got, err := s.UserByEmail(ctx, "ACCOUNT@EXAMPLE.COM "); err != nil || got != u {
		t.Fatalf("lookup by normalized email returned id=%d, error=%v", got.ID, err)
	}
	dup := u
	dup.Email, dup.Name, dup.PasswordHash = "Other <ACCOUNT@example.com>", "Other", fixtureHash(t)
	_, err := s.CreateUser(ctx, dup)
	requireErr(t, err, store.ErrEmailInUse)
	if got, err := s.UserByID(ctx, u.ID); err != nil || got != u {
		t.Fatalf("duplicate changed original account: id=%d, error=%v", got.ID, err)
	}
	_, err = s.UserByEmail(ctx, "other@example.com")
	requireErr(t, err, store.ErrNotFound)

	for _, invalid := range []auth.User{
		{Email: "not a mailbox", Role: auth.RoleMember, PasswordHash: u.PasswordHash},
		{Email: "other@example.com", Role: "other", PasswordHash: u.PasswordHash},
		{Email: "other@example.com", Role: auth.RoleMember, PasswordHash: "not a hash"},
	} {
		if _, err := s.CreateUser(ctx, invalid); err == nil {
			t.Fatal("invalid user was saved")
		}
	}
	if _, err := s.UserByEmail(ctx, "not a mailbox"); err == nil {
		t.Fatal("invalid lookup address was accepted")
	}
	list, err := s.ListUsers(ctx)
	if err != nil || len(list) != 1 || list[0] != u {
		t.Fatalf("list users returned %d records, error=%v", len(list), err)
	}
}

func TestStoreSetupClaimIsSingleUse(t *testing.T) {
	s, _ := contractStore(t)
	ctx := t.Context()
	_, err := s.SetupState(ctx)
	requireErr(t, err, store.ErrNotFound)
	if n, err := s.UserCount(ctx); err != nil || n != 0 {
		t.Fatalf("fresh user count = %d, error=%v", n, err)
	}
	digest := auth.HashToken(auth.NewToken())
	other := auth.HashToken(auth.NewToken())
	if issued, err := s.IssueSetupToken(ctx, digest); err != nil || !issued {
		t.Fatalf("issue setup = %v, error=%v", issued, err)
	}
	if issued, err := s.IssueSetupToken(ctx, other); err != nil || issued {
		t.Fatalf("second issue = %v, error=%v", issued, err)
	}
	if state, err := s.SetupState(ctx); err != nil || state.Consumed || state.TokenHash != digest {
		t.Fatalf("unexpected state before claim: consumed=%v, error=%v", state.Consumed, err)
	}
	u := auth.User{
		Name: "Sample", Email: " FIRST@example.com ", Role: auth.RoleMember,
		PasswordHash: fixtureHash(t), Disabled: true, MustChangePassword: true,
	}
	_, err = s.ClaimSetup(ctx, other, u)
	requireErr(t, err, store.ErrSetupUnavailable)
	type claim struct {
		user auth.User
		err  error
	}
	start, done := make(chan struct{}), make(chan claim, 2)
	for i := range 2 {
		go func() {
			<-start
			candidate := u
			if i == 1 {
				candidate.Email = "second@example.com"
			}
			user, err := s.ClaimSetup(ctx, digest, candidate)
			done <- claim{user, err}
		}()
	}
	close(start)
	success := 0
	for range 2 {
		c := <-done
		if c.err != nil {
			requireErr(t, c.err, store.ErrSetupUnavailable)
			continue
		}
		success++
		if c.user.ID <= 0 || c.user.Role != auth.RoleAdmin || c.user.Disabled || c.user.MustChangePassword {
			t.Fatalf("setup winner did not become an enabled admin: id=%d role=%q", c.user.ID, c.user.Role)
		}
		if c.user.Email != "first@example.com" && c.user.Email != "second@example.com" {
			t.Errorf("setup did not normalize email: %q", c.user.Email)
		}
	}
	if n, err := s.UserCount(ctx); success != 1 || n != 1 || err != nil {
		t.Fatalf("setup winners=%d users=%d, error=%v", success, n, err)
	}
	if state, err := s.SetupState(ctx); err != nil || !state.Consumed || state.TokenHash != digest {
		t.Fatalf("unexpected state after claim: consumed=%v, error=%v", state.Consumed, err)
	}
	if issued, err := s.IssueSetupToken(ctx, other); err != nil || issued {
		t.Fatalf("issue after claim = %v, error=%v", issued, err)
	}
}

func TestStoreSetupGuards(t *testing.T) {
	digest := auth.HashToken(auth.NewToken())
	t.Run("cannot issue with users and no token", func(t *testing.T) {
		s, _ := contractStore(t)
		ctx := t.Context()
		createUser(t, s, "existing@example.com", auth.RoleMember)
		if issued, err := s.IssueSetupToken(ctx, digest); err != nil || issued {
			t.Fatalf("issue with users present = %v, error=%v", issued, err)
		}
		_, err := s.SetupState(ctx)
		requireErr(t, err, store.ErrNotFound)
	})
	t.Run("cannot claim after another account exists", func(t *testing.T) {
		s, _ := contractStore(t)
		ctx := t.Context()
		if issued, err := s.IssueSetupToken(ctx, digest); err != nil || !issued {
			t.Fatalf("issue setup = %v, error=%v", issued, err)
		}
		existing := createUser(t, s, "existing@example.com", auth.RoleMember)
		candidate := auth.User{Name: "Sample", Email: "candidate@example.com", PasswordHash: existing.PasswordHash}
		_, err := s.ClaimSetup(ctx, digest, candidate)
		requireErr(t, err, store.ErrSetupUnavailable)
		if n, err := s.UserCount(ctx); err != nil || n != 1 {
			t.Fatalf("refused claim changed user count to %d: %v", n, err)
		}
	})
	t.Run("consumed token stays spent with no users", func(t *testing.T) {
		s, d := contractStore(t)
		ctx := t.Context()
		if issued, err := s.IssueSetupToken(ctx, digest); err != nil || !issued {
			t.Fatalf("issue setup = %v, error=%v", issued, err)
		}
		candidate := auth.User{Name: "Sample", Email: "candidate@example.com", PasswordHash: fixtureHash(t)}
		u, err := s.ClaimSetup(ctx, digest, candidate)
		requireErr(t, err, nil)
		removeUserRow(t, d, u.ID)
		_, err = s.ClaimSetup(ctx, digest, candidate)
		requireErr(t, err, store.ErrSetupUnavailable)
		if n, err := s.UserCount(ctx); err != nil || n != 0 {
			t.Fatalf("refused claim changed user count to %d: %v", n, err)
		}
	})
}

func TestStoreLastAdminGuard(t *testing.T) {
	s, _ := contractStore(t)
	ctx := t.Context()
	admin := createUser(t, s, "admin@example.com", auth.RoleAdmin)
	assertUser := func(want auth.User) {
		t.Helper()
		if got, err := s.UserByID(ctx, want.ID); err != nil || got != want {
			t.Fatalf("unexpected state for user %d: role=%q disabled=%v error=%v", want.ID, got.Role, got.Disabled, err)
		}
	}
	requireErr(t, s.SetDisabled(ctx, admin.ID, true), store.ErrLastAdmin)
	assertUser(admin)
	requireErr(t, s.SetRole(ctx, admin.ID, auth.RoleMember), store.ErrLastAdmin)
	assertUser(admin)
	requireErr(t, s.SetRole(ctx, admin.ID, auth.RoleAdmin), nil)
	assertUser(admin)

	other := createUser(t, s, "other@example.com", auth.RoleAdmin)
	requireErr(t, s.SetDisabled(ctx, other.ID, true), nil)
	other.Disabled = true
	assertUser(other)
	requireErr(t, s.SetDisabled(ctx, admin.ID, true), store.ErrLastAdmin)
	requireErr(t, s.SetRole(ctx, admin.ID, auth.RoleMember), store.ErrLastAdmin)
	assertUser(admin)
	requireErr(t, s.SetDisabled(ctx, other.ID, true), nil)
	assertUser(other)
	requireErr(t, s.SetDisabled(ctx, admin.ID, false), nil)
	requireErr(t, s.SetDisabled(ctx, other.ID, false), nil)
	other.Disabled = false
	assertUser(other)
	requireErr(t, s.SetRole(ctx, other.ID, auth.RoleMember), nil)
	other.Role = auth.RoleMember
	assertUser(other)

	requireErr(t, s.SetDisabled(ctx, other.ID, true), nil)
	other.Disabled = true
	requireErr(t, s.SetRole(ctx, other.ID, auth.RoleAdmin), nil)
	requireErr(t, s.SetRole(ctx, other.ID, auth.RoleMember), nil)
	assertUser(other)
	requireErr(t, s.SetDisabled(ctx, other.ID, false), nil)
	other.Disabled = false
	assertUser(other)
	requireErr(t, s.SetRole(ctx, other.ID, auth.RoleAdmin), nil)
	other.Role = auth.RoleAdmin
	assertUser(other)
}

func TestStoreConcurrentLastAdminGuard(t *testing.T) {
	s, _ := contractStore(t)
	ctx := t.Context()
	a := createUser(t, s, "first@example.com", auth.RoleAdmin)
	b := createUser(t, s, "second@example.com", auth.RoleAdmin)
	start, done := make(chan struct{}), make(chan error, 2)
	go func() { <-start; done <- s.SetRole(ctx, a.ID, auth.RoleMember) }()
	go func() { <-start; done <- s.SetDisabled(ctx, b.ID, true) }()
	close(start)
	var successes int
	for range 2 {
		if err := <-done; err == nil {
			successes++
		} else {
			requireErr(t, err, store.ErrLastAdmin)
		}
	}
	if n, err := s.EnabledAdminCount(ctx); successes != 1 || n != 1 || err != nil {
		t.Fatalf("changes=%d enabled admins=%d, error=%v", successes, n, err)
	}
	requireErr(t, s.SetRole(ctx, -1, auth.RoleMember), store.ErrNotFound)
	requireErr(t, s.SetDisabled(ctx, -1, true), store.ErrNotFound)
}

func TestStorePasswordChangesAndRevocation(t *testing.T) {
	s, d := contractStore(t)
	ctx := t.Context()
	u := createUser(t, s, "member@example.com", auth.RoleMember)
	v := createUser(t, s, "other@example.com", auth.RoleMember)
	now := time.Date(2025, 1, 2, 3, 4, 5, 123456000, time.UTC)
	active := auth.HashToken(createSession(t, s, u, now))
	other := auth.HashToken(createSession(t, s, u, now))
	unrelated := auth.HashToken(createSession(t, s, v, now))
	changedHash := fixtureHash(t)

	if ok, err := s.RehashPassword(ctx, u.ID, v.PasswordHash, changedHash); err != nil || ok {
		t.Fatalf("stale rehash = %v, error=%v", ok, err)
	}
	if ok, err := s.RehashPassword(ctx, u.ID, u.PasswordHash, changedHash); err != nil || !ok {
		t.Fatalf("fresh rehash = %v, error=%v", ok, err)
	}
	if got, err := s.UserByID(ctx, u.ID); err != nil || got.PasswordHash != changedHash || !got.MustChangePassword {
		t.Fatalf("rehash did not preserve account state: %v", err)
	}
	resetHash := fixtureHash(t)
	requireErr(t, s.ChangePassword(ctx, u.ID, u.PasswordHash, resetHash, active), store.ErrCredentialsChanged)
	if rows := rawRows(t, d); len(rows) != 3 {
		t.Fatalf("rejected password change altered sessions: %d remain", len(rows))
	}
	requireErr(t, s.ChangePassword(ctx, u.ID, changedHash, u.PasswordHash, active), nil)
	got, _, err := s.LookupSession(ctx, active)
	if err != nil || got.MustChangePassword || got.PasswordHash != u.PasswordHash {
		t.Fatalf("own password change lost current session or didn't update user: %v", err)
	}
	_, _, err = s.LookupSession(ctx, other)
	requireErr(t, err, store.ErrNotFound)
	requireErr(t, s.ResetPassword(ctx, u.ID, resetHash), nil)
	_, _, err = s.LookupSession(ctx, active)
	requireErr(t, err, store.ErrNotFound)
	requireErr(t, s.CreateSession(ctx, u.ID, u.PasswordHash, active, now), store.ErrCredentialsChanged)
	if got, err := s.UserByID(ctx, u.ID); err != nil || got.PasswordHash != resetHash || !got.MustChangePassword {
		t.Fatalf("reset did not install a temporary password: %v", err)
	}
	if _, _, err := s.LookupSession(ctx, unrelated); err != nil {
		t.Fatalf("password change revoked another user's session: %v", err)
	}
	if rows := rawRows(t, d); len(rows) != 1 {
		t.Fatalf("revoked sessions still persisted: %d rows", len(rows))
	}
}

func TestStoreDisableAndDeleteSessions(t *testing.T) {
	s, d := contractStore(t)
	ctx := t.Context()
	u := createUser(t, s, "member@example.com", auth.RoleMember)
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	a := auth.HashToken(createSession(t, s, u, now))
	b := auth.HashToken(createSession(t, s, u, now))
	requireErr(t, s.DeleteSession(ctx, a), nil)
	_, _, err := s.LookupSession(ctx, a)
	requireErr(t, err, store.ErrNotFound)
	_, _, err = s.LookupSession(ctx, b)
	requireErr(t, err, nil)

	requireErr(t, s.SetDisabled(ctx, u.ID, true), nil)
	requireErr(t, s.CreateSession(ctx, u.ID, u.PasswordHash, a, now), store.ErrCredentialsChanged)
	_, _, err = s.LookupSession(ctx, b)
	requireErr(t, err, store.ErrNotFound)
	requireErr(t, s.SetDisabled(ctx, u.ID, false), nil)
	_, _, err = s.LookupSession(ctx, b)
	requireErr(t, err, store.ErrNotFound)
	requireErr(t, s.CreateSession(ctx, u.ID, u.PasswordHash, a, now), nil)
	requireErr(t, s.RevokeSessions(ctx, u.ID), nil)
	if rows := rawRows(t, d); len(rows) != 0 {
		t.Fatalf("revocation left %d rows", len(rows))
	}
}

func TestStoreLookupRejectsDisabledUserWithSession(t *testing.T) {
	s, d := contractStore(t)
	ctx := t.Context()
	u := createUser(t, s, "member@example.com", auth.RoleMember)
	hash := auth.HashToken(createSession(t, s, u, time.Now()))
	_, _, err := s.LookupSession(ctx, hash)
	requireErr(t, err, nil)

	// Bypass session revocation to exercise the separate lookup guard.
	disableUserRow(t, d, u.ID)
	if rows := rawRows(t, d); len(rows) != 1 {
		t.Fatalf("expected the session row to survive, found %d", len(rows))
	}
	_, _, err = s.LookupSession(ctx, hash)
	requireErr(t, err, store.ErrNotFound)
}

func TestStoreSessionTouch(t *testing.T) {
	s, d := contractStore(t)
	ctx := t.Context()
	u := createUser(t, s, "member@example.com", auth.RoleMember)
	now := time.Date(2025, 1, 2, 3, 4, 5, 123456000, time.FixedZone("offset", 3600))
	token := createSession(t, s, u, now)
	hash := auth.HashToken(token)
	wantDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	if rows := rawRows(t, d); len(rows) != 1 || strings.Contains(rows[0], token) || !strings.Contains(rows[0], wantDigest) {
		t.Fatal("stored session did not contain only the digest as its credential")
	}
	assertSeen := func(want time.Time) {
		t.Helper()
		got, sess, err := s.LookupSession(ctx, hash)
		if err != nil || got.ID != u.ID || sess.UserID != u.ID || !sess.CreatedAt.Equal(now) || !sess.LastSeen.Equal(want) {
			t.Fatalf("session = %+v, error=%v; want last seen %v", sess, err, want)
		}
	}
	assertSeen(now)
	touch := now.Add(session.TouchEvery)
	requireErr(t, s.TouchSession(ctx, hash, touch.Add(-time.Microsecond)), nil)
	assertSeen(now)
	requireErr(t, s.TouchSession(ctx, hash, touch), nil)
	assertSeen(touch)
	backdate(t, d, token, now, touch.Add(time.Hour))
	requireErr(t, s.TouchSession(ctx, hash, touch), nil)
	assertSeen(touch)
}

func TestStoreSweepSessions(t *testing.T) {
	s, d := contractStore(t)
	ctx := t.Context()
	u := createUser(t, s, "member@example.com", auth.RoleMember)
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	life := session.Default
	tests := []struct {
		name          string
		created, seen time.Time
		expired       bool
	}{
		{"absolute deadline", now.Add(-life.Absolute), now, true},
		{"idle deadline", now.Add(-life.Idle - time.Minute), now.Add(-life.Idle), true},
		{"just inside idle", now.Add(-life.Idle - time.Minute), now.Add(-life.Idle + time.Microsecond), false},
		{"activity before creation", now.Add(-life.Idle + time.Microsecond), now.Add(-life.Idle - time.Minute), false},
		{"just inside absolute", now.Add(-life.Absolute + time.Microsecond), now, false},
	}
	hashes := make([]auth.TokenHash, 0, len(tests))
	for _, tc := range tests {
		token := createSession(t, s, u, now)
		backdate(t, d, token, tc.created, tc.seen)
		hashes = append(hashes, auth.HashToken(token))
	}
	if n, err := s.SweepSessions(ctx, life, now); err != nil || n != 2 {
		t.Fatalf("sweep removed %d, want 2: %v", n, err)
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := s.LookupSession(ctx, hashes[i])
			if tc.expired {
				requireErr(t, err, store.ErrNotFound)
			} else {
				requireErr(t, err, nil)
			}
		})
	}
	token := createSession(t, s, u, now)
	backdate(t, d, token, now.Add(-life.Absolute+time.Minute), now.Add(-life.Idle))
	if n, err := s.SweepSessions(ctx, session.Lifetime{Absolute: life.Absolute, IdleOff: true}, now); err != nil || n != 0 {
		t.Fatalf("absolute-only sweep removed %d, want 0: %v", n, err)
	}
}
