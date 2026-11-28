//go:build unix

package db_test

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/config"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/db"
)

func TestBackupWaitsForLock(t *testing.T) {
	root := t.TempDir()
	cfg := config.DB{DBPath: filepath.Join(root, "app.db"), BackupDir: filepath.Join(root, "backups"), BackupKeep: 1}
	d, err := db.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// Hold the named lock file as another process would. The kit takes the
	// path literally (unlike MigrationLock, which adds a suffix).
	held, err := os.OpenFile(cfg.DBPath+".backup-lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	logs := &logBuffer{}
	loop, err := d.Backups(cfg).Start(t.Context(), slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer loop.Stop()
	time.Sleep(200 * time.Millisecond)
	// The kit may create the folder first; it must not write a copy yet.
	if files, err := os.ReadDir(cfg.BackupDir); err != nil && !errors.Is(err, os.ErrNotExist) || len(files) != 0 {
		t.Fatalf("backup written while lock held: %v, %v", files, err)
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	// The run was waiting on the lock, not skipped: it finishes once freed.
	awaitLog(t, logs, "database backup saved")
}
