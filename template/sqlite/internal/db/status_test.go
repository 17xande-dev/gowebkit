package db

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/config"
	"modernc.org/sqlite"
)

func TestStatusConnectionCannotWrite(t *testing.T) {
	cfg := config.DB{DBPath: filepath.Join(t.TempDir(), "app.db")}
	if err := Migrate(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	pool, err := openStatusPool(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.ExecContext(t.Context(), `CREATE TABLE status_probe (id INTEGER) STRICT`)
	var sqliteErr *sqlite.Error
	// Test SQLite's access mode on a writable file; chmod alone doesn't
	// prove it because a read/write open can fall back to read-only.
	const sqliteReadOnly = 8
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqliteReadOnly {
		t.Fatalf("write on status connection = %v, want SQLITE_READONLY", err)
	}
}

// Exists must answer without creating the file or its directory: the reset
// command relies on that to refuse an uninitialised database.
func TestExistsDoesNotCreateAnything(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nested", "app.db")
	if ok, err := Exists(t.Context(), config.DB{DBPath: missing}); err != nil || ok {
		t.Fatalf("Exists(missing) = %v, %v; want false, nil", ok, err)
	}
	if _, err := os.Stat(filepath.Dir(missing)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Exists created the directory: %v", err)
	}
	cfg := config.DB{DBPath: filepath.Join(dir, "app.db")}
	if err := Migrate(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if ok, err := Exists(t.Context(), cfg); err != nil || !ok {
		t.Fatalf("Exists(migrated) = %v, %v; want true, nil", ok, err)
	}
}
