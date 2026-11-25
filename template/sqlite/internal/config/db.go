package config

import (
	"path/filepath"
	"strings"
)

// DB describes the SQLite file and backup retention without opening either.
type DB struct {
	DBPath     string
	BackupDir  string
	BackupKeep int
}

func loadDB(e *env) DB {
	path := e.text("DB_PATH", filepath.Join("data", "app.db"))
	dir := e.text("BACKUP_DIR", filepath.Join(filepath.Dir(path), "backups"))
	keep := e.integer("BACKUP_KEEP", 14, 0, 0)
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) || path == ":memory:" || strings.HasPrefix(path, "file:") {
		e.reject("DB_PATH must be a file path")
	}
	if keep > 0 && (strings.TrimSpace(dir) == "" || strings.ContainsRune(dir, 0)) {
		e.reject("BACKUP_DIR must be a directory path when backups are enabled")
	}
	return DB{DBPath: path, BackupDir: dir, BackupKeep: keep}
}
