package db_test

import (
	"bytes"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/config"
	"github.com/17xande-dev/gowebkit/template/sqlite/internal/db"
)

// logBuffer is a log sink the test can poll while the backup loop writes to it.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func awaitLog(t *testing.T, logs *logBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(logs.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("log never contained %q: %s", want, logs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStartMakesAnOnlineBackup(t *testing.T) {
	root := t.TempDir()
	cfg := config.DB{DBPath: filepath.Join(root, "app.db"), BackupDir: filepath.Join(root, "backups")}
	d, err := db.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	logs := &logBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	if loop, err := d.Backups(cfg).Start(t.Context(), log); loop != nil || err != nil {
		t.Fatalf("retention zero did not disable backups: %v, %v", loop, err)
	}
	cfg.BackupKeep = 1
	if _, err := d.ExecContext(t.Context(), `INSERT INTO users (name, email, role, password_hash) VALUES (?, ?, ?, ?)`,
		"Backup check", "account@example.com", "admin", "fixture"); err != nil {
		t.Fatal(err)
	}
	loop, err := d.Backups(cfg).Start(t.Context(), log)
	if err != nil || loop == nil {
		t.Fatalf("start: %v, %v", loop, err)
	}
	defer loop.Stop()
	awaitLog(t, logs, "database backup saved")
	if strings.Contains(logs.String(), root) {
		t.Fatalf("log names the full path, want the file name only: %s", logs)
	}
	files, err := os.ReadDir(cfg.BackupDir)
	if err != nil || len(files) != 1 {
		t.Fatalf("backup files = %v, %v", files, err)
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
	if err := os.MkdirAll(cfg.BackupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app-2025-01-02.db", "app-2025-03-09.db", "app-2025-02-01.db"} {
		if err := os.WriteFile(filepath.Join(cfg.BackupDir, name), nil, 0o600); err != nil {
			t.Fatal(err)
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
