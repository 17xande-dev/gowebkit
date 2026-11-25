package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/auth"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/db"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/dbtest"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/store"
)

// winRehashRace models a writer that wins between credential verification and
// the rehash write: the competing hash lands first and the rehash's guarded
// update then changes no row. A BEFORE trigger rewrites the row itself and
// returns NULL, which skips the statement's own update.
func winRehashRace(t *testing.T, d *db.DB, replacement string) {
	t.Helper()
	literal := "'" + strings.ReplaceAll(replacement, "'", "''") + "'"
	for _, stmt := range []string{
		`CREATE FUNCTION win_rehash_race() RETURNS trigger LANGUAGE plpgsql AS $fn$
		BEGIN
			UPDATE users SET password_hash = ` + literal + ` WHERE id = OLD.id;
			RETURN NULL;
		END $fn$`,
		`CREATE TRIGGER win_rehash_race BEFORE UPDATE OF password_hash ON users
			FOR EACH ROW WHEN (NEW.password_hash <> ` + literal + `) EXECUTE FUNCTION win_rehash_race()`,
	} {
		if _, err := d.Pool().Exec(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
}

// corruptStoredHash stores a hash the password library cannot parse, so a test
// can see whether a password check ran without comparing timings.
func corruptStoredHash(t *testing.T, d *db.DB, userID int64) {
	t.Helper()
	if _, err := d.Pool().Exec(t.Context(), `UPDATE users SET password_hash = 'invalid-hash' WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationCommands(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() // A migration command must not try to bind a server.
	setTestEnv(t, ln.Addr().(*net.TCPAddr).Port)
	dbtest.SetEnv(t)

	command := func(flag, want string) {
		t.Helper()
		var out, stderr bytes.Buffer
		if err := run([]string{flag}, &out, &stderr); err != nil || stderr.Len() != 0 || !strings.HasPrefix(out.String(), want) {
			t.Fatalf("%s: stdout=%q stderr=%q error=%v; want %q", flag, &out, &stderr, err, want)
		}
	}
	command("-migrate-status", "0001 pending\n")
	if exists, err := db.Exists(t.Context(), mustConfig(t).DB); err != nil || exists {
		t.Fatalf("-migrate-status created migration history: exists=%v, error=%v", exists, err)
	}
	command("-migrate", "Database migrations applied\n")
	command("-migrate-status", "0001 applied ")
}

func mustConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestMigrationDownRefusals(t *testing.T) {
	for _, name := range []string{"unmigrated", "baseline"} {
		t.Run(name, func(t *testing.T) {
			setTestEnv(t, 8080)
			dbtest.SetEnv(t)
			cfg := mustConfig(t)
			var want string
			switch name {
			case "unmigrated":
				want = "nothing to roll back"
			case "baseline":
				d, err := db.Open(t.Context(), cfg.DB)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = d.Close() })
				if _, err := d.Pool().Exec(t.Context(), `INSERT INTO users (name, email, role, password_hash)
					VALUES ('Sample', 'admin@example.com', 'admin', 'fixture')`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					var n int
					if err := d.Pool().QueryRow(context.Background(), `SELECT count(*) FROM users`).Scan(&n); err != nil || n != 1 {
						t.Errorf("baseline user count = %d: %v", n, err)
					}
				})
				want = "refusing to roll back the baseline migration; restore from a backup instead"
			}
			var out, stderr bytes.Buffer
			err := run([]string{"-migrate-down"}, &out, &stderr)
			if err == nil || !strings.Contains(err.Error(), want) || out.Len() != 0 || stderr.Len() != 0 {
				t.Errorf("rollback stdout=%q stderr=%q error=%v; want %q and no success output", &out, &stderr, err, want)
			}
			if name == "unmigrated" {
				if exists, err := db.Exists(t.Context(), cfg.DB); err != nil || exists {
					t.Errorf("rollback left migration history behind: exists=%v, error=%v", exists, err)
				}
			}
		})
	}
}

// connectionCounter stands in for a database server and records every
// connection attempt, so a test can see whether anything tried to connect.
func connectionCounter(t *testing.T) (url string, attempts *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	attempts = new(atomic.Int64)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			attempts.Add(1)
			_ = conn.Close()
		}
	}()
	return fmt.Sprintf("postgres://app:app@%s/app?sslmode=disable&connect_timeout=2", ln.Addr()), attempts
}

func TestDatabaseModesValidateBeforeOpening(t *testing.T) {
	setTestEnv(t, 8080)
	for _, mode := range []string{"", "-check-config", "-migrate", "-migrate-status", "-migrate-down"} {
		name := mode
		if name == "" {
			name = "server"
		}
		t.Run(name, func(t *testing.T) {
			url, attempts := connectionCounter(t)
			t.Setenv("DATABASE_URL", url)
			t.Setenv("BASE_URL", "https://app.example/")
			var args []string
			if mode != "" {
				args = []string{mode}
			}
			if err := run(args, io.Discard, io.Discard); err == nil || !strings.HasPrefix(err.Error(), "configuration:") {
				t.Fatalf("%s opened before validating configuration: %v", name, err)
			}
			if n := attempts.Load(); n != 0 {
				t.Fatalf("%s tried to connect to the database %d times before validation", name, n)
			}
		})
	}
}

type listeningAddress chan string

func (addresses listeningAddress) Write(p []byte) (int, error) {
	var event struct{ Address string }
	if err := json.Unmarshal(p, &event); err != nil {
		return 0, err
	}
	if event.Address != "" {
		addresses <- event.Address
	}
	return len(p), nil
}

func TestRunServerHealthzUsesDatabase(t *testing.T) {
	setTestEnv(t, 8080)
	dbtest.SetEnv(t)
	cfg := mustConfig(t)
	cfg.Port = 0
	addresses := make(listeningAddress, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServer(ctx, cfg, newLogger(addresses, cfg), db.Open, time.Hour) }()

	var addr string
	select {
	case addr = <-addresses:
	case err := <-done:
		t.Fatalf("server exited before listening: %v", err)
	case <-ctx.Done():
		t.Fatal("server did not listen")
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != "ok\n" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("healthz: status=%d body=%q error=%v", resp.StatusCode, body, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}

	// The same handler reports unavailable when a real pool stops answering.
	d := dbtest.Open(t)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	srv, err := newServer(cfg, newLogger(io.Discard, cfg), store.New(d), d.Ping)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz with closed database = %d, want 503", rec.Code)
	}
}

// PostgreSQL backups are made outside the application. The admin page must say
// that rather than "turned off", and the server must not start a backup loop.
func TestRunServerSaysBackupsAreMadeOutsideTheApplication(t *testing.T) {
	setTestEnv(t, 8080)
	dbtest.SetEnv(t)
	cfg := mustConfig(t)
	cfg.Port = 0
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	events := &lockedLog{address: make(listeningAddress, 1)}
	token := auth.NewToken()
	open := func(ctx context.Context, cfgDB config.DB) (*db.DB, error) {
		d, err := db.Open(ctx, cfgDB)
		if err != nil {
			return nil, err
		}
		accounts := store.New(d)
		hash, err := auth.HashPassword(ctx, "backup-note-passphrase")
		if err == nil {
			var u auth.User
			u, err = accounts.CreateUser(ctx, auth.User{Name: "Sample", Email: "admin-account@example.com", Role: auth.RoleAdmin, PasswordHash: hash})
			if err == nil {
				err = accounts.CreateSession(ctx, u.ID, hash, auth.HashToken(token), time.Now())
			}
		}
		if err != nil {
			return nil, fmt.Errorf("seed: %w (close: %v)", err, d.Close())
		}
		return d, nil
	}
	done := make(chan error, 1)
	go func() { done <- runServer(ctx, cfg, slog.New(slog.NewJSONHandler(events, nil)), open, time.Hour) }()
	var addr string
	select {
	case addr = <-events.address:
	case err := <-done:
		t.Fatalf("server exited before listening: %v", err)
	case <-ctx.Done():
		t.Fatal("server did not listen")
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/admin/users", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	page, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("admin page: status=%d error=%v", resp.StatusCode, err)
	}
	if !strings.Contains(string(page), "pg_dump") || strings.Contains(string(page), "turned off") {
		t.Fatalf("the admin page does not describe PostgreSQL backups correctly: %q", page)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(events.String(), "makes no database backups") {
		t.Fatalf("startup did not log that the application makes no backups: %s", events.String())
	}
}

// lockedLog collects log lines and reports the listening address. The server
// logs from its own goroutines while the test reads, so access is locked.
type lockedLog struct {
	address listeningAddress
	mu      sync.Mutex
	lines   []string
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.lines = append(l.lines, string(p))
	l.mu.Unlock()
	return l.address.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "")
}

func TestBackupLoopRunsAtStartupAndOnTicks(t *testing.T) {
	now := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC)
	ticks := make(chan time.Time, 2)
	want := []time.Time{now, now.Add(time.Hour), now.AddDate(0, 0, 1)}
	ticks <- want[1]
	ticks <- want[2]
	close(ticks)
	var logs bytes.Buffer
	var got []time.Time
	backup := func(_ context.Context, at time.Time) (string, bool, error) {
		got = append(got, at)
		return "", false, nil
	}
	runBackups(t.Context(), slog.New(slog.NewTextHandler(&logs, nil)), backup, now, ticks)
	if !slices.Equal(got, want) || logs.Len() != 0 {
		t.Fatalf("backup calls = %v, want %v; logs=%q", got, want, &logs)
	}
}
