package db_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/db"
	"github.com/17xande-dev/gowebkit/template/postgres/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// holdMigrationLock takes the lock a boot takes, on a connection of the test's
// own, and gives it back when the test ends or release is called.
func holdMigrationLock(t *testing.T, cfg config.DB) (release func()) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), cfg.ConnectionString())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const key = `hashtextextended('app.migrate:' || coalesce(current_schema(), ''), 0)`
	if _, err := conn.Exec(t.Context(), `SELECT pg_advisory_lock(`+key+`)`); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			if _, err := conn.Exec(context.WithoutCancel(t.Context()), `SELECT pg_advisory_unlock(`+key+`)`); err != nil {
				t.Error(err)
			}
			conn.Release()
			pool.Close()
		})
	}
	t.Cleanup(release)
	return release
}

// A pool of one connection would hold it for the migration lock and wait for
// another to migrate with, for ever. It is refused up front, before any
// connection is tried (the address below is unreachable on purpose).
func TestPoolOfOneConnectionIsRefusedBeforeConnecting(t *testing.T) {
	cfg := dbtest.ConfigWithURL("postgres://app:app@127.0.0.1:1/app?sslmode=disable&pool_max_conns=1")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := db.Open(ctx, cfg)
	if err == nil || !strings.Contains(err.Error(), "pool_max_conns must be at least 2") {
		t.Fatalf("Open = %v, want the pool-size error", err)
	}
	if _, err := db.Status(ctx, cfg); err == nil || !strings.Contains(err.Error(), "pool_max_conns must be at least 2") {
		t.Fatalf("Status = %v, want the pool-size error", err)
	}
}

func tableNames(t *testing.T, cfg config.DB) []string {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), cfg.ConnectionString())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	rows, err := conn.Query(t.Context(), `SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestOpenWaitsForMigrationLockBeforeTouchingTheSchema(t *testing.T) {
	cfg := dbtest.Config(t)
	holdMigrationLock(t, cfg)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	d, err := db.Open(ctx, cfg)
	if err == nil {
		_ = d.Close()
		t.Fatal("Open returned while the migration lock was held")
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "migration lock") {
		t.Fatalf("error = %v, want a timeout naming the migration lock", err)
	}
	if names := tableNames(t, cfg); len(names) != 0 {
		t.Fatalf("Open touched the schema without the lock: %v", names)
	}
}

// Concurrent boots queue behind one another. The test holds the lock, starts
// several boots, requires every one of them to be waiting, and only then lets
// go: without the lock they would race to create the same tables.
func TestConcurrentOpensQueueOnTheMigrationLock(t *testing.T) {
	cfg := dbtest.Config(t)
	release := holdMigrationLock(t, cfg)
	const boots = 6
	results := make(chan error, boots)
	pools := make(chan *db.DB, boots)
	for range boots {
		go func() {
			d, err := db.Open(t.Context(), cfg)
			pools <- d
			results <- err
		}()
	}
	waitForSessions(t, cfg, "pg_advisory_lock", boots, results)
	release()
	for range boots {
		if err := <-results; err != nil {
			t.Fatalf("a queued boot failed: %v", err)
		}
		if d := <-pools; d != nil {
			t.Cleanup(func() { _ = d.Close() })
		}
	}
	status, err := db.Status(t.Context(), cfg)
	if err != nil || len(status) != 1 || !status[0].Applied {
		t.Fatalf("status after the boots = %+v, %v", status, err)
	}
}

// waitForSessions waits until want sessions of the schema in cfg are blocked in
// a statement containing fragment.
func waitForSessions(t *testing.T, cfg config.DB, fragment string, want int, done <-chan error) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), cfg.ConnectionString())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
	defer cancel()
	for {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE application_name = current_setting('application_name')
			AND wait_event_type = 'Lock' AND query LIKE '%' || $1 || '%' AND pid <> pg_backend_pid()`, fragment).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == want {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("a boot returned (%v) while the lock was held (%d of %d waiting)", err, n, want)
		case <-ctx.Done():
			t.Fatalf("%d boots waiting, want %d", n, want)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestStatusAndExistsDoNotChangeAnUnmigratedSchema(t *testing.T) {
	cfg := dbtest.Config(t)
	status, err := db.Status(t.Context(), cfg)
	if err != nil || len(status) != 1 || status[0].Version != 1 || status[0].Applied {
		t.Fatalf("status = %+v, %v; want one pending migration", status, err)
	}
	if exists, err := db.Exists(t.Context(), cfg); err != nil || exists {
		t.Fatalf("exists = %v, %v", exists, err)
	}
	if names := tableNames(t, cfg); len(names) != 0 {
		t.Fatalf("Status or Exists created %v", names)
	}
	if err := db.Migrate(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if exists, err := db.Exists(t.Context(), cfg); err != nil || !exists {
		t.Fatalf("exists after migrating = %v, %v", exists, err)
	}
	status, err = db.Status(t.Context(), cfg)
	if err != nil || len(status) != 1 || !status[0].Applied || status[0].AppliedAt.IsZero() {
		t.Fatalf("status after migrating = %+v, %v", status, err)
	}
}

// The wait for another process's migration is bounded by its own limit, not
// only by the caller's context (a boot has none): a stuck holder fails the
// boot with a clear message instead of hanging it.
func TestMigrationLockWaitIsBounded(t *testing.T) {
	cfg := dbtest.Config(t)
	holdMigrationLock(t, cfg)
	defer db.SetLockWait(200 * time.Millisecond)()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	start := time.Now()
	d, err := db.Open(ctx, cfg)
	if err == nil {
		_ = d.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "another process may be migrating") {
		t.Fatalf("Open error = %v, want the lock-wait message", err)
	}
	if ctx.Err() != nil || time.Since(start) > 10*time.Second {
		t.Fatalf("the wait was not bounded by lockWait (took %v)", time.Since(start))
	}
}

func TestStatusWaitsForTheMigrationLock(t *testing.T) {
	cfg := dbtest.Config(t)
	holdMigrationLock(t, cfg)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := db.Status(ctx, cfg); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "migration lock") {
		t.Fatalf("error = %v, want a timeout naming the migration lock", err)
	}
}

func TestNewerDatabaseIsRefused(t *testing.T) {
	cfg := dbtest.Config(t)
	if err := db.Migrate(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(t.Context(), cfg.ConnectionString())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	if _, err := conn.Exec(t.Context(), `INSERT INTO goose_db_version (version_id, is_applied) VALUES (2, true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Status(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "newer than this build") {
		t.Fatalf("Status error = %v", err)
	}
	if d, err := db.Open(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "newer than this build") {
		if d != nil {
			_ = d.Close()
		}
		t.Fatalf("Open error = %v", err)
	}
	// A rollback with an older build names the cause instead of goose's
	// "version not found", and changes nothing.
	var before int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM goose_db_version`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	err = db.MigrateDown(t.Context(), cfg)
	if err == nil || !strings.Contains(err.Error(), "newer than this build") || !strings.Contains(err.Error(), "image that shipped it") {
		t.Fatalf("MigrateDown error = %v", err)
	}
	var after int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM goose_db_version`).Scan(&after); err != nil || after != before {
		t.Fatalf("history rows %d -> %d, %v; the refused rollback must change nothing", before, after, err)
	}
}

func TestTablesWithoutHistoryAreRefused(t *testing.T) {
	cfg := dbtest.Config(t)
	conn, err := pgx.Connect(t.Context(), cfg.ConnectionString())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context())
	if _, err := conn.Exec(t.Context(), `CREATE TABLE users (id bigint)`); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(t.Context(), cfg)
	if err == nil || !strings.Contains(err.Error(), "migration history is unknown") {
		if d != nil {
			_ = d.Close()
		}
		t.Fatalf("error = %v", err)
	}
	if names := tableNames(t, cfg); len(names) != 1 {
		t.Fatalf("the refused open changed the schema: %v", names)
	}
}

func TestPingAndBackups(t *testing.T) {
	d := dbtest.Open(t)
	if err := d.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if set := d.Backups(config.DB{}); set != nil {
		t.Fatal("PostgreSQL has no in-application backup set")
	}
	var set *db.BackupSet
	if _, ok, err := set.Newest(); ok || err != nil {
		t.Fatalf("a nil set reports a backup: %v, %v", ok, err)
	}
}

// Each constraint is part of the schema's contract; the store relies on them
// to refuse what its own checks might miss.
func TestSchemaConstraints(t *testing.T) {
	d := dbtest.Open(t)
	hash := strings.Repeat("00", 32)
	for _, tt := range []struct {
		name, setup, stmt string
	}{
		{"unknown role", ``, `INSERT INTO users (name, email, role, password_hash) VALUES ('A', 'a@example.com', 'owner', 'x')`},
		{"email differing only by case", `INSERT INTO users (name, email, role, password_hash) VALUES ('A', 'a@example.com', 'member', 'x')`,
			`INSERT INTO users (name, email, role, password_hash) VALUES ('B', 'A@Example.com', 'member', 'x')`},
		{"session digest of the wrong length", `INSERT INTO users (name, email, role, password_hash) VALUES ('A', 'a@example.com', 'member', 'x')`,
			`INSERT INTO sessions (token_hash, user_id, created_at, last_seen) VALUES (decode('00', 'hex'), 1, now(), now())`},
		{"session for no user", ``, `INSERT INTO sessions (token_hash, user_id, created_at, last_seen) VALUES (decode('` + hash + `', 'hex'), 99, now(), now())`},
		{"second setup row", `INSERT INTO setup (id, token_hash) VALUES (1, decode('` + hash + `', 'hex'))`,
			`INSERT INTO setup (id, token_hash) VALUES (2, decode('` + hash + `', 'hex'))`},
		{"setup digest of the wrong length", ``, `INSERT INTO setup (id, token_hash) VALUES (1, decode('00', 'hex'))`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// A failed statement aborts a transaction, so each case rolls back.
			tx, err := d.Pool().Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			if tt.setup != "" {
				if _, err := tx.Exec(t.Context(), tt.setup); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			if _, err := tx.Exec(t.Context(), tt.stmt); err == nil {
				t.Fatal("the database accepted the row")
			}
		})
	}
}
