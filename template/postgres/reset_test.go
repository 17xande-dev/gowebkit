package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/db"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/dbtest"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
	"github.com/17xande-dev/password"
)

// resetFixture writes a migrated database holding one admin with a live
// session, configured through the environment as the real command is.
func resetFixture(t *testing.T) (cfg config.Config, user auth.User, session auth.TokenHash) {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	setTestEnv(t, ln.Addr().(*net.TCPAddr).Port)
	dbtest.SetEnv(t)
	t.Setenv("PASSWORD_CONCURRENCY", "3")
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(t.Context(), cfg.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := store.New(d)
	hash, err := auth.HashPassword(t.Context(), "original sample passphrase")
	if err != nil {
		t.Fatal(err)
	}
	user, err = s.CreateUser(t.Context(), auth.User{Name: "Operator", Email: "operator@example.com", Role: auth.RoleAdmin, PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	session = auth.HashToken(auth.NewToken())
	if err := s.CreateSession(t.Context(), user.ID, hash, session, time.Now()); err != nil {
		t.Fatal(err)
	}
	return cfg, user, session
}

func TestResetPasswordForcesChangeAndEndsSessions(t *testing.T) {
	const fresh = "operator chosen passphrase"
	passwordFile := func(t *testing.T) string {
		path := filepath.Join(t.TempDir(), "password")
		if err := os.WriteFile(path, []byte(fresh+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, tt := range []struct {
		name  string
		args  func(*testing.T) []string
		stdin string
	}{
		{"standard input", func(*testing.T) []string { return []string{"-reset-password", " Operator@Example.COM "} }, fresh + "\n"},
		{"file", func(t *testing.T) []string {
			return []string{"-reset-password", "operator@example.com", "-password-file", passwordFile(t)}
		}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, user, session := resetFixture(t)
			var applied []int
			saved := setPasswordConcurrency
			setPasswordConcurrency = func(n int) { applied = append(applied, n) }
			t.Cleanup(func() { setPasswordConcurrency = saved })

			var stdout, stderr bytes.Buffer
			if err := execute(tt.args(t), strings.NewReader(tt.stdin), &stdout, &stderr); err != nil {
				t.Fatalf("reset: %v (stderr %q)", err, &stderr)
			}
			if len(applied) != 1 || applied[0] != 3 {
				t.Fatalf("hashing concurrency applied %v, want [3] from the configuration", applied)
			}
			if strings.Contains(stdout.String()+stderr.String(), fresh) {
				t.Fatal("the password was printed")
			}

			d, err := db.Open(t.Context(), cfg.DB)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			s := store.New(d)
			got, err := s.UserByID(t.Context(), user.ID)
			if err != nil || !got.MustChangePassword {
				t.Fatalf("a password set by the operator must force a change: %+v, %v", got, err)
			}
			if ok, _, err := password.Check(t.Context(), got.PasswordHash, fresh); !ok || err != nil {
				t.Fatalf("new password not installed: %v, %v", ok, err)
			}
			if _, _, err := s.LookupSession(t.Context(), session); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("session survived the reset: %v", err)
			}
		})
	}
}

func TestResetPasswordRefusals(t *testing.T) {
	cfg, user, session := resetFixture(t)
	run := func(stdin string, args ...string) error {
		return execute(args, strings.NewReader(stdin), &bytes.Buffer{}, &bytes.Buffer{})
	}
	for _, tt := range []struct {
		name, stdin string
		args        []string
		want        string
	}{
		{"short password", "short\n", []string{"-reset-password", "operator@example.com"}, "at least 12 characters"},
		{"no password", "", []string{"-reset-password", "operator@example.com"}, "no password"},
		{"too long", strings.Repeat("x", 2000) + "\n", []string{"-reset-password", "operator@example.com"}, "too long"},
		{"unknown account", "a sample passphrase\n", []string{"-reset-password", "nobody@example.com"}, "no account"},
		{"bad email", "a sample passphrase\n", []string{"-reset-password", "not a mailbox"}, "valid mailbox"},
		{"missing file", "", []string{"-reset-password", "operator@example.com", "-password-file", filepath.Join(t.TempDir(), "none")}, "password file"},
		{"file without reset", "", []string{"-password-file", "x"}, "only used with"},
		{"two modes", "", []string{"-reset-password", "operator@example.com", "-migrate"}, "only one command mode"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := run(tt.stdin, tt.args...); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
	d, err := db.Open(t.Context(), cfg.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := store.New(d)
	got, err := s.UserByID(t.Context(), user.ID)
	if err != nil || got.PasswordHash != user.PasswordHash || got.MustChangePassword {
		t.Fatalf("a refused reset changed the account: %v", err)
	}
	if _, _, err := s.LookupSession(t.Context(), session); err != nil {
		t.Fatalf("a refused reset ended the session: %v", err)
	}
}

// An empty database must be refused, not initialised: opening it would run the
// migrations and then report a missing account.
func TestResetPasswordDoesNotInitialiseADatabase(t *testing.T) {
	resetFixture(t)
	dbtest.SetEnv(t) // a different, empty schema
	err := execute([]string{"-reset-password", "operator@example.com"}, strings.NewReader("a sample passphrase\n"), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not been migrated") {
		t.Fatalf("error = %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := db.Exists(t.Context(), cfg.DB); err != nil || exists {
		t.Fatalf("the command left migration history behind: exists=%v, error=%v", exists, err)
	}
}

// Resetting a disabled account works but cannot let the person in, so the
// operator is told rather than left to find out at the sign-in page.
func TestResetPasswordSaysWhenTheAccountIsDisabled(t *testing.T) {
	cfg, _, _ := resetFixture(t)
	d, err := db.Open(t.Context(), cfg.DB)
	if err != nil {
		t.Fatal(err)
	}
	s := store.New(d)
	hash, err := auth.HashPassword(t.Context(), "original sample passphrase")
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.CreateUser(t.Context(), auth.User{Name: "Member", Email: "member@example.com", Role: auth.RoleMember, PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDisabled(t.Context(), member.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := execute([]string{"-reset-password", "member@example.com"}, strings.NewReader("a sample passphrase\n"), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "account is disabled") || !strings.Contains(stdout.String(), "Users page") {
		t.Fatalf("output does not mention the disabled account: %q", &stdout)
	}
}
