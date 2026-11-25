package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/config"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/db"
	"github.com/17xande-dev/sqlitekit"
)

func TestOnlineBackupAndRetention(t *testing.T) {
	root := t.TempDir()
	cfg := config.DB{DBPath: filepath.Join(root, "app.db"), BackupDir: filepath.Join(root, "backups")}
	d, err := db.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Backups(cfg) != nil {
		t.Fatal("retention zero did not disable backups")
	}
	cfg.BackupKeep = 1
	backups := d.Backups(cfg)
	if _, err := d.ExecContext(t.Context(), `INSERT INTO users (name, email, role, password_hash) VALUES (?, ?, ?, ?)`,
		"Backup check", "account@example.com", "admin", "fixture"); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		at   time.Time
		made bool
		name string
	}{
		{now, true, "app-2025-01-02.db"},
		{now.Add(time.Hour), false, "app-2025-01-02.db"},
		{now.AddDate(0, 0, 1), true, "app-2025-01-03.db"},
	} {
		path, made, err := backups.Run(t.Context(), tc.at)
		if err != nil || made != tc.made || path != filepath.Join(cfg.BackupDir, tc.name) {
			t.Fatalf("backup at %v: path=%q made=%v error=%v", tc.at, path, made, err)
		}
	}
	files, err := os.ReadDir(cfg.BackupDir)
	if err != nil || len(files) != 1 || files[0].Name() != "app-2025-01-03.db" {
		t.Fatalf("retention left %v, error=%v", files, err)
	}
	snapshot, err := sql.Open("sqlite", filepath.Join(cfg.BackupDir, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var name string
	if err := snapshot.QueryRowContext(t.Context(), `SELECT name FROM users`).Scan(&name); err != nil || name != "Backup check" {
		t.Fatalf("online backup did not capture a committed row: %q, %v", name, err)
	}
}

func TestBackupWaitsForLock(t *testing.T) {
	root := t.TempDir()
	cfg := config.DB{DBPath: filepath.Join(root, "app.db"), BackupDir: filepath.Join(root, "backups"), BackupKeep: 1}
	d, err := db.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	release, err := sqlitekit.MigrationLock(t.Context(), cfg.DBPath+".backups")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, made, err := d.Backups(cfg).Run(ctx, time.Now()); made || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("backup while lock held: made=%v error=%v", made, err)
	}
	if _, err := os.Stat(cfg.BackupDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup directory touched while lock held: %v", err)
	}
}

func TestNewestBackupDate(t *testing.T) {
	root := t.TempDir()
	cfg := config.DB{DBPath: filepath.Join(root, "app.db"), BackupDir: filepath.Join(root, "backups"), BackupKeep: 5}
	d, err := db.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	var disabled *db.BackupSet
	if _, ok, err := disabled.Newest(); ok || err != nil {
		t.Fatalf("a disabled set reported a backup: %v, %v", ok, err)
	}
	backups := d.Backups(cfg)
	if _, ok, err := backups.Newest(); ok || err != nil {
		t.Fatalf("a missing directory reported a backup: %v, %v", ok, err)
	}
	for _, day := range []time.Time{
		time.Date(2025, 1, 2, 12, 0, 0, 0, time.UTC),
		time.Date(2025, 3, 9, 12, 0, 0, 0, time.UTC),
		time.Date(2025, 2, 1, 12, 0, 0, 0, time.UTC),
	} {
		if _, made, err := backups.Run(t.Context(), day); err != nil || !made {
			t.Fatalf("backup for %v: made=%v error=%v", day, made, err)
		}
	}
	// Files that are not backups must not count, even with a later date.
	if err := os.WriteFile(filepath.Join(cfg.BackupDir, "other-2030-01-01.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok, err := backups.Newest()
	if err != nil || !ok || got.Format("2006-01-02") != "2025-03-09" {
		t.Fatalf("newest backup = %v, %v, %v; want 2025-03-09", got, ok, err)
	}
}
