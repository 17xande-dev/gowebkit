package db_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/config"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/db"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/dbtest"
	"github.com/17xande-dev/sqlitekit"
	"github.com/17xande-dev/sqlitekit/goosestrict"
	"github.com/pressly/goose/v3/database"
)

func TestConcurrentFreshOpens(t *testing.T) {
	const n = 16
	cfg := config.DB{DBPath: filepath.Join(t.TempDir(), "app.db")}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	// A boot holding the lock but not yet querying the file must keep all
	// other boots out. SQLite's own timing can mask the missing flock.
	release, err := sqlitekit.MigrationLock(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := make(chan struct{})
	ready := make(chan struct{}, n)
	type result struct {
		d   *db.DB
		err error
	}
	done := make(chan result, n)
	for range n {
		go func() {
			ready <- struct{}{}
			<-start
			d, err := db.Open(ctx, cfg)
			done <- result{d, err}
		}()
	}
	for range n {
		<-ready
	}
	close(start)
	hold := time.NewTimer(250 * time.Millisecond)
	defer hold.Stop()
	var opened int
	consume := func(r result) {
		if r.err != nil {
			t.Errorf("concurrent Open: %v", r.err)
			return
		}
		opened++
		// Don't checkpoint WAL by closing a successful pool during first opens.
		t.Cleanup(func() { _ = r.d.Close() })
	}
	remaining := n
	select {
	case r := <-done:
		t.Error("Open returned while the migration lock was still held")
		consume(r)
		remaining--
	case <-hold.C:
	}
	if _, err := os.Stat(cfg.DBPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("database touched while the migration lock was still held: %v", err)
	}
	if err := release(); err != nil {
		t.Error(err)
	}
	for range remaining {
		consume(<-done)
	}
	if opened != n {
		t.Fatalf("opened %d/%d pools", opened, n)
	}
	assertStatus(t, cfg, true, true)
}

func TestAppMigrationPrecedesOutboxMigration(t *testing.T) {
	cfg := config.DB{DBPath: filepath.Join(t.TempDir(), "app.db")}
	pool, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	history, err := goosestrict.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := history.CreateVersionTable(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := history.Insert(ctx, pool, database.InsertRequest{Version: 0}); err != nil {
		t.Fatal(err)
	}
	// The outbox refuses its unversioned table. The app baseline must
	// already have committed when that refusal reaches the caller.
	if _, err := pool.ExecContext(ctx, `CREATE TABLE outbox_jobs (id INTEGER) STRICT`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, cfg); err == nil || !strings.Contains(err.Error(), "migration history is unknown") {
		t.Fatalf("outbox migration did not refuse unknown history: %v", err)
	}
	if version, err := history.GetLatestVersion(ctx, pool); err != nil || version != 1 {
		t.Fatalf("application did not migrate before the outbox failed: version=%d, error=%v", version, err)
	}
}

func TestOpenWaitsForMigrationLockBeforeFirstQuery(t *testing.T) {
	cfg := config.DB{DBPath: filepath.Join(t.TempDir(), "app.db")}
	release, err := sqlitekit.MigrationLock(t.Context(), cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	d, err := db.Open(ctx, cfg)
	if d != nil {
		d.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), cfg.DBPath+".migrate-lock") {
		t.Fatalf("Open error = %v, want deadline naming the lock", err)
	}
	if _, err := os.Stat(cfg.DBPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database touched before acquiring lock: %v", err)
	}
}

func TestSchemaTablesAreStrict(t *testing.T) {
	d := dbtest.Open(t)
	rows, err := d.QueryContext(t.Context(), `SELECT name, sql FROM sqlite_master
		WHERE type = 'table' AND name NOT GLOB 'sqlite_*'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			t.Fatal(err)
		}
		n++
		if !strings.HasSuffix(strings.TrimSpace(strings.ToUpper(ddl)), ") STRICT") {
			t.Errorf("table %s is not STRICT: %s", name, ddl)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 6 {
		t.Errorf("checked %d tables, want at least 6 including the queue and both histories", n)
	}
}

func TestMigrateUpDownUp(t *testing.T) {
	cfg := config.DB{DBPath: filepath.Join(t.TempDir(), "app.db")}
	if err := db.Migrate(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, cfg, true, true)
	// The baseline Down SQL remains reversible in an isolated database
	// even though the operator command will not drop those tables.
	d, err := db.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, err := goosestrict.NewProvider(d.DB, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Down(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, cfg, false, true)
	if got := tableCount(t, cfg.DBPath); got != 3 {
		t.Fatalf("application Down kept %d tables, want app history, queue and queue history", got)
	}
	if err := db.Migrate(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, cfg, true, true)
	if got := tableCount(t, cfg.DBPath); got < 6 {
		t.Fatalf("second Up left only %d tables", got)
	}
}

func TestNewDatabaseIsPrivate(t *testing.T) {
	cfg := config.DB{DBPath: filepath.Join(t.TempDir(), "app.db")}
	if err := db.Migrate(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("new database permissions = %04o, want 0600", got)
	}
}

func TestStatusWaitsForMigrationLock(t *testing.T) {
	cfg := config.DB{DBPath: filepath.Join(t.TempDir(), "app.db")}
	release, err := sqlitekit.MigrationLock(t.Context(), cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := db.Status(ctx, cfg); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), cfg.DBPath+".migrate-lock") {
		t.Fatalf("Status error = %v, want deadline naming the lock", err)
	}
}

func TestStatusRefusesNewerDatabase(t *testing.T) {
	cfg := config.DB{DBPath: filepath.Join(t.TempDir(), "app.db")}
	d, err := db.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	history, err := goosestrict.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := history.Insert(t.Context(), d.DB, database.InsertRequest{Version: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Status(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "newer than this build") {
		t.Fatalf("Status error = %v, want newer than this build", err)
	}
	// The strict store already names the cause for a rollback too.
	err = db.MigrateDown(t.Context(), cfg)
	if err == nil || !strings.Contains(err.Error(), "written by a newer build") {
		t.Fatalf("MigrateDown error = %v", err)
	}
}

func TestStatusReadsReadOnlyFile(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("requires Unix file permissions and an unprivileged user")
	}
	cfg := config.DB{DBPath: filepath.Join(t.TempDir(), "app.db")}
	if err := db.Migrate(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	// A WAL reader can need sidecars even with a read-only connection.
	// Use a clean rollback-journal copy to check status on a bare file.
	pool, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var journal string
	if err := pool.QueryRowContext(t.Context(), `PRAGMA journal_mode=DELETE`).Scan(&journal); err != nil || journal != "delete" {
		t.Fatalf("journal mode = %q, want delete: %v", journal, err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.DBPath, 0o400); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, cfg, true, true)
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(cfg.DBPath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("status created %s sidecar: %v", suffix, err)
		}
	}
}

func TestStatusDoesNotInitializeDatabase(t *testing.T) {
	cfg := config.DB{DBPath: filepath.Join(t.TempDir(), "missing", "sample #?.db")}
	assertStatus(t, cfg, false, false)
	if _, err := os.Stat(filepath.Dir(cfg.DBPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created the database directory: %v", err)
	}
	if err := os.Mkdir(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, cfg, false, false)
	if _, err := os.Stat(cfg.DBPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created the database: %v", err)
	}
	if err := os.WriteFile(cfg.DBPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, cfg, false, false)
	if got := tableCount(t, cfg.DBPath); got != 0 {
		t.Fatalf("status created %d tables in an empty database", got)
	}
	if err := db.Migrate(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, cfg, true, true)
}

func assertStatus(t *testing.T, cfg config.DB, appApplied, queueApplied bool) {
	t.Helper()
	rows, err := db.Status(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("migration status = %+v, want app and outbox migrations", rows)
	}
	for i, applied := range []bool{appApplied, queueApplied} {
		if row := rows[i]; row.Version != 1 || row.Outbox != (i == 1) || row.Applied != applied || row.AppliedAt.IsZero() == applied {
			t.Errorf("migration status row %d = %+v, want applied=%v", i, row, applied)
		}
	}
}

func tableCount(t *testing.T, path string) int {
	t.Helper()
	dsn, err := sqlitekit.DSN(path, sqlitekit.Options{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var n int
	err = pool.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name NOT GLOB 'sqlite_*'`).Scan(&n)
	if err != nil {
		t.Fatal(fmt.Errorf("count tables: %w", err))
	}
	return n
}
