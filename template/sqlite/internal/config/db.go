package config

import (
	"math"
	"path/filepath"
	"strings"

	"github.com/17xande-dev/env"
)

// DB describes the SQLite file and backup retention without opening either.
type DB struct {
	DBPath     string
	BackupDir  string
	BackupKeep int
}

func loadDB(r *env.Reader) DB {
	path := r.String("DB_PATH", filepath.Join("data", "app.db"))
	dir := r.String("BACKUP_DIR", filepath.Join(filepath.Dir(path), "backups"))
	keep := r.Int("BACKUP_KEEP", 14, 0, math.MaxInt)
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) || path == ":memory:" || strings.HasPrefix(path, "file:") {
		r.Fail("DB_PATH", "must be a file path")
	}
	if keep > 0 && (strings.TrimSpace(dir) == "" || strings.ContainsRune(dir, 0)) {
		r.Fail("BACKUP_DIR", "must be a directory path when backups are enabled")
	}
	return DB{DBPath: path, BackupDir: dir, BackupKeep: keep}
}
