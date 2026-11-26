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
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/config"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/db"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/dbtest"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/store"
)

// winRehashRace models a writer that wins between credential verification and
// the rehash write: the competing hash lands first and the rehash's guarded
// update then changes no row.
func winRehashRace(t *testing.T, d *db.DB, replacement string) {
	t.Helper()
	_, err := d.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER win_rehash_race
		BEFORE UPDATE OF password_hash ON users
		WHEN NEW.password_hash <> '%[1]s'
		BEGIN
			UPDATE users SET password_hash = '%[1]s' WHERE id = OLD.id;
			SELECT RAISE(IGNORE);
		END`, replacement))
	if err != nil {
		t.Fatal(err)
	}
}

// corruptStoredHash stores a hash the password library cannot parse, so a test
// can see whether a password check ran without comparing timings.
func corruptStoredHash(t *testing.T, d *db.DB, userID int64) {
	t.Helper()
	if _, err := d.ExecContext(t.Context(), `UPDATE users SET password_hash = 'invalid-hash' WHERE id = ?`, userID); err != nil {
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
	command("-migrate", "Database migrations applied\n")
	command("-migrate-status", "0001 applied ")
	command("-migrate-status", "\noutbox 0001 applied ")
}

func TestMigrationDownRefusals(t *testing.T) {
	for _, name := range []string{"missing file", "missing directory", "empty", "baseline"} {
		t.Run(name, func(t *testing.T) {
			setTestEnv(t, 8080)
			dbtest.SetEnv(t)
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			var want string
			// Set by the baseline case: the queue's tables and history as
			// the rollback found them.
			var queueState func() string
			switch name {
			case "missing file":
				want = `no database at "app.db"`
			case "missing directory":
				cfg.DBPath = filepath.Join(t.TempDir(), "missing", "app.db")
				t.Setenv("DB_PATH", cfg.DBPath)
				want = `no database at "app.db"`
			case "empty":
				if err := os.WriteFile(cfg.DBPath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				want = "nothing to roll back"
			case "baseline":
				d, err := db.Open(t.Context(), cfg.DB)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = d.Close() })
				if _, err := d.ExecContext(t.Context(), `INSERT INTO users (name, email, role, password_hash)
					VALUES ('Sample', 'admin@example.com', 'admin', 'fixture')`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					var n int
					if err := d.QueryRowContext(context.Background(), `SELECT count(*) FROM users`).Scan(&n); err != nil || n != 1 {
						t.Errorf("baseline user count = %d: %v", n, err)
					}
				})
				want = "refusing to roll back the baseline migration; restore from a backup instead"
				queueState = func() string {
					var jobs, versions int
					err := d.QueryRowContext(context.Background(), `SELECT (SELECT count(*) FROM outbox_jobs),
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
			err = run([]string{"-migrate-down"}, &out, &stderr)
			if err == nil || !strings.Contains(err.Error(), want) || out.Len() != 0 || stderr.Len() != 0 {
				t.Errorf("rollback stdout=%q stderr=%q error=%v; want %q and no success output", &out, &stderr, err, want)
			}
			if queueState != nil {
				if after := queueState(); after != queueBefore {
					t.Errorf("rollback changed the queue: before %s, after %s", queueBefore, after)
				}
			}
			switch name {
			case "missing file":
				if files, err := os.ReadDir(filepath.Dir(cfg.DBPath)); err != nil || len(files) != 0 {
					t.Errorf("rollback touched an empty directory: %v, %v", files, err)
				}
			case "missing directory":
				if _, err := os.Stat(filepath.Dir(cfg.DBPath)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("rollback touched a missing database directory: %v", err)
				}
			}
		})
	}
}

func TestDatabaseModesValidateBeforeOpening(t *testing.T) {
	setTestEnv(t, 8080)
	for _, mode := range []string{"", "-check-config", "-migrate", "-migrate-status", "-migrate-down"} {
		name := mode
		if name == "" {
			name = "server"
		}
		t.Run(name, func(t *testing.T) {
			dbtest.SetEnv(t)
			dir := filepath.Join(t.TempDir(), "new")
			t.Setenv("DB_PATH", filepath.Join(dir, "app.db"))
			t.Setenv("BASE_URL", "https://app.example/")
			t.Setenv("COOKIE_SECURE", "true")
			var args []string
			if mode != "" {
				args = []string{mode}
			}
			if err := run(args, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "BASE_URL: must be a canonical origin") {
				t.Fatalf("%s opened before validating configuration: %v", name, err)
			}
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("touched database directory before validation: %v", err)
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
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
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

type backupEvents struct {
	address listeningAddress
	files   chan string
	release <-chan struct{}
	saved   int
}

func (e *backupEvents) Write(p []byte) (int, error) {
	var event struct{ Msg, File string }
	if err := json.Unmarshal(p, &event); err != nil {
		return 0, err
	}
	if event.Msg == "database backup saved" {
		e.saved++
		if e.saved <= 2 {
			e.files <- event.File
		}
		if e.saved == 2 {
			// Hold the real backup goroutine when it logs a completed copy.
			<-e.release
		}
	}
	return e.address.Write(p)
}

func TestRunServerRunsAndJoinsBackups(t *testing.T) {
	setTestEnv(t, 8080)
	dbtest.SetEnv(t)
	t.Setenv("BACKUP_KEEP", "1")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = 0
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	blocked := make(chan struct{})
	release := sync.OnceFunc(func() { close(blocked) })
	defer release()
	events := &backupEvents{address: make(listeningAddress, 1), files: make(chan string, 2), release: blocked}
	opened := make(chan *db.DB, 1)
	open := func(ctx context.Context, cfg config.DB) (*db.DB, error) {
		d, err := db.Open(ctx, cfg)
		opened <- d
		return d, err
	}
	done := make(chan error, 1)
	go func() { done <- runServer(ctx, cfg, newLogger(events, cfg), open, 20*time.Millisecond) }()

	var addr string
	select {
	case addr = <-events.address:
	case err := <-done:
		t.Fatalf("server exited before listening: %v", err)
	case <-ctx.Done():
		t.Fatal("server did not listen")
	}
	d := <-opened
	nextBackup := func(when string) string {
		t.Helper()
		select {
		case name := <-events.files:
			date := strings.TrimSuffix(strings.TrimPrefix(name, "app-"), ".db")
			if _, err := time.Parse(time.DateOnly, date); err != nil || name != "app-"+date+".db" {
				t.Fatalf("invalid %s backup name %q: %v", when, name, err)
			}
			return filepath.Join(cfg.BackupDir, name)
		case err := <-done:
			t.Fatalf("server exited before %s backup: %v", when, err)
		case <-ctx.Done():
			t.Fatalf("no %s backup was written", when)
		}
		return ""
	}
	startup := nextBackup("startup")
	// Backups are daily; removing the startup copy lets a tick write
	// another one without depending on the wall clock changing days.
	if err := os.Remove(startup); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if info, err := os.Stat(nextBackup("periodic")); err != nil || info.Size() == 0 {
		t.Fatalf("periodic backup was not written to disk: %v", err)
	}

	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	awaitClosedListener(t, "127.0.0.1:"+port)
	select {
	case err := <-done:
		t.Fatalf("server returned before backup work finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := d.Ping(t.Context()); err != nil {
		t.Fatalf("database closed before backup work finished: %v", err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not return after backup work finished")
	}
	if err := d.Ping(t.Context()); err == nil {
		t.Fatal("database still open after server returned")
	}
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
