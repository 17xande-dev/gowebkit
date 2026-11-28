package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		if err := run([]string{flag}, &out, &stderr); err != nil || stderr.Len() != 0 || !strings.Contains(out.String(), want) {
			t.Fatalf("%s: stdout=%q stderr=%q error=%v; want %q", flag, &out, &stderr, err, want)
		}
	}
	command("-migrate-status", "0001 pending\noutbox 0001 pending\n")
	if exists, err := db.Exists(t.Context(), mustConfig(t).DB); err != nil || exists {
		t.Fatalf("-migrate-status created migration history: exists=%v, error=%v", exists, err)
	}
	command("-migrate", "Database migrations applied\n")
	command("-migrate-status", "0001 applied ")
	command("-migrate-status", "\noutbox 0001 applied ")
}

func TestOutboxMigrationStatus(t *testing.T) {
	setTestEnv(t, 8080)
	dbtest.SetEnv(t)
	// The CLI has always printed the wall-clock value in the zone-less
	// goose column with a Z suffix, even for a non-UTC database session.
	t.Setenv("DATABASE_URL", os.Getenv("DATABASE_URL")+"&options=-c%20TimeZone%3DAsia%2FTokyo")
	d, err := db.Open(t.Context(), mustConfig(t).DB)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var out, stderr bytes.Buffer
	for _, applied := range []bool{true, false} {
		// An existing row counts as applied even if a legacy writer set
		// is_applied to false: status must agree with the queue migrator.
		if _, err := d.Pool().Exec(t.Context(), `UPDATE outbox_goose_version
			SET is_applied = $1, tstamp = '2020-01-02 03:04:05' WHERE version_id = 1`, applied); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		err := run([]string{"-migrate-status"}, &out, &stderr)
		if err != nil || stderr.Len() != 0 || !strings.HasSuffix(out.String(), "\noutbox 0001 applied 2020-01-02T03:04:05Z\n") {
			t.Fatalf("is_applied=%t: stdout=%q stderr=%q error=%v; want queue migration and its stored timestamp", applied, &out, &stderr, err)
		}
	}

	if _, err := d.Pool().Exec(t.Context(), `INSERT INTO outbox_goose_version
		(version_id, is_applied) VALUES (2, true)`); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err = run([]string{"-migrate-status"}, &out, &stderr)
	if err == nil || !strings.Contains(err.Error(), "migration 2 is newer than this build") || out.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("newer history: stdout=%q stderr=%q error=%v; want a newer migration error and no status output", &out, &stderr, err)
	}
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
			// Set by the baseline case: the queue's tables and history as
			// the rollback found them.
			var queueState func() string
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
				queueState = func() string {
					var jobs, versions int
					err := d.Pool().QueryRow(context.Background(), `SELECT (SELECT count(*) FROM outbox_jobs),
						(SELECT count(*) FROM outbox_goose_version)`).Scan(&jobs, &versions)
					if err != nil {
						t.Fatalf("queue tables are missing after rollback: %v", err)
					}
					return fmt.Sprintf("jobs=%d versions=%d", jobs, versions)
				}
			}
			var queueBefore string
			if queueState != nil {
				queueBefore = queueState()
			}
			var out, stderr bytes.Buffer
			err := run([]string{"-migrate-down"}, &out, &stderr)
			if err == nil || !strings.Contains(err.Error(), want) || out.Len() != 0 || stderr.Len() != 0 {
				t.Errorf("rollback stdout=%q stderr=%q error=%v; want %q and no success output", &out, &stderr, err, want)
			}
			if queueState != nil {
				if after := queueState(); after != queueBefore {
					t.Errorf("rollback changed the queue: before %s, after %s", queueBefore, after)
				}
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
			t.Setenv("COOKIE_SECURE", "true")
			var args []string
			if mode != "" {
				args = []string{mode}
			}
			if err := run(args, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "BASE_URL: must be a canonical origin") {
				t.Fatalf("%s opened before validating configuration: %v", name, err)
			}
			if n := attempts.Load(); n != 0 {
				t.Fatalf("%s tried to connect to the database %d times before validation", name, n)
			}
		})
	}
}

func TestRunServerWarnsWhenUsingDevelopmentDatabase(t *testing.T) {
	setTestEnv(t, 8080)
	t.Setenv("DATABASE_URL", "")
	if err := os.Unsetenv("DATABASE_URL"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL_FILE", "")

	const developmentURL = "postgres://app:app@localhost:5432/app?sslmode=disable"
	file := filepath.Join(t.TempDir(), "url")
	if err := os.WriteFile(file, []byte(developmentURL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		secure bool
		env    map[string]string
		warn   bool
	}{
		{"secure and unset", true, nil, true},
		{"secure and empty", true, map[string]string{"DATABASE_URL": ""}, true},
		{"insecure and unset", false, nil, false},
		{"secure and explicit", true, map[string]string{"DATABASE_URL": developmentURL}, false},
		{"secure and file", true, map[string]string{"DATABASE_URL": "", "DATABASE_URL_FILE": file}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.secure {
				t.Setenv("BASE_URL", "https://app.example")
				t.Setenv("COOKIE_SECURE", "true")
			}
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			var stdout, stderr bytes.Buffer
			if err := run([]string{"-check-config"}, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if stdout.String() != "Configuration OK\n" || stderr.Len() != 0 {
				t.Fatal("-check-config should confirm valid settings without a startup warning")
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			var beforeOpen string
			stop := errors.New("stop before connecting")
			open := func(context.Context, config.DB) (*db.DB, error) {
				beforeOpen = logs.String()
				return nil, stop
			}
			if err := runServer(t.Context(), cfg, newLogger(&logs, cfg), open, time.Hour); !errors.Is(err, stop) {
				t.Fatalf("expected to reach the database opener, got %v", err)
			}
			if !tt.warn {
				if logs.Len() != 0 {
					t.Error("unexpected database warning")
				}
				return
			}
			var event struct{ Level, Msg string }
			if err := json.Unmarshal([]byte(beforeOpen), &event); err != nil {
				t.Fatalf("expected one startup warning before connecting: %v", err)
			}
			if event.Level != "WARN" ||
				event.Msg != "using the development PostgreSQL URL with COOKIE_SECURE=true; set DATABASE_URL or DATABASE_URL_FILE for this deployment" {
				t.Error("expected a WARN with guidance for configuring PostgreSQL")
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
