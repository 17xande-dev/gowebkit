// Package dbtest opens migrated SQLite files for tests.
package dbtest

import (
	"path/filepath"
	"testing"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/config"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/db"
)

// SetEnv points process configuration at a fresh test database.
func SetEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DB_PATH", filepath.Join(dir, "app.db"))
	t.Setenv("BACKUP_DIR", filepath.Join(dir, "backups"))
	t.Setenv("BACKUP_KEEP", "0")
}

// Open gives each call its own real database and closes it after the test.
func Open(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(t.Context(), config.DB{DBPath: filepath.Join(t.TempDir(), "app.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d
}
