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
	assertStatus(t, cfg, true)
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
	if n < 4 {
		t.Errorf("checked %d tables, want at least 4 including migration history", n)
	}
}

func TestMigrateUpDownUp(t *testing.T) {
	cfg := config.DB{DBPath: filepath.Join(t.TempDir(), "app.db")}
	if err := db.Migrate(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, cfg, true)
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
	assertStatus(t, cfg, false)
	if got := tableCount(t, cfg.DBPath); got != 1 {
		t.Fatalf("Down kept %d tables, want only the migration history", got)
	}
	if err := db.Migrate(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, cfg, true)
	if got := tableCount(t, cfg.DBPath); got < 4 {
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
	assertStatus(t, cfg, true)
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(cfg.DBPath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("status created %s sidecar: %v", suffix, err)
		}
	}
}

func TestStatusDoesNotInitializeDatabase(t *testing.T) {
	cfg := config.DB{DBPath: filepath.Join(t.TempDir(), "missing", "sample #?.db")}
	assertStatus(t, cfg, false)
	if _, err := os.Stat(filepath.Dir(cfg.DBPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created the database directory: %v", err)
	}
	if err := os.Mkdir(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, cfg, false)
	if _, err := os.Stat(cfg.DBPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created the database: %v", err)
	}
	if err := os.WriteFile(cfg.DBPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, cfg, false)
	if got := tableCount(t, cfg.DBPath); got != 0 {
		t.Fatalf("status created %d tables in an empty database", got)
	}
	if err := db.Migrate(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, cfg, true)
}

func assertStatus(t *testing.T, cfg config.DB, applied bool) {
	t.Helper()
	rows, err := db.Status(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Version != 1 || rows[0].Applied != applied || rows[0].AppliedAt.IsZero() == applied {
		t.Fatalf("migration status = %+v, want a single migration with applied=%v", rows, applied)
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
