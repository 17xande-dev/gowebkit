package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/config"
	"github.com/17xande-dev/sqlitekit"
)

// BackupsNote is the line the admin page shows when Backups returns nil.
const BackupsNote = "Backups are turned off."

// BackupSet writes online copies of the open database.
type BackupSet struct {
	db       *DB
	settings sqlitekit.Backups
	lockPath string
}

// Backups returns nil when backups are disabled.
func (d *DB) Backups(cfg config.DB) *BackupSet {
	if cfg.BackupKeep == 0 {
		return nil
	}
	return &BackupSet{
		db:       d,
		settings: sqlitekit.Backups{Dir: cfg.BackupDir, Prefix: "app-", Layout: "2006-01-02", Keep: cfg.BackupKeep},
		lockPath: cfg.DBPath + ".backups",
	}
}

// Newest reports the date of the newest backup file. It is safe to call on a
// nil set (backups disabled) and then reports none, so a page can show "no
// backups" without a separate check.
func (b *BackupSet) Newest() (time.Time, bool, error) {
	if b == nil {
		return time.Time{}, false, nil
	}
	files, err := b.settings.List()
	if err != nil || len(files) == 0 {
		return time.Time{}, false, err
	}
	// List is newest first and only holds names that parse in this layout.
	name := strings.TrimSuffix(strings.TrimPrefix(files[0].Name, b.settings.Prefix), ".db")
	date, err := time.Parse(b.settings.Layout, name)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse backup date: %w", err)
	}
	return date, true, nil
}

// Run creates today's backup if absent and applies retention.
func (b *BackupSet) Run(ctx context.Context, now time.Time) (_ string, _ bool, err error) {
	// Two servers sharing the database can overlap on restart. The kit's
	// backup routine requires one caller; use a separate lock so a large
	// backup doesn't make a new server wait to apply migrations.
	wait, cancel := context.WithTimeout(ctx, time.Minute)
	release, err := sqlitekit.MigrationLock(wait, b.lockPath)
	cancel()
	if err != nil {
		return "", false, fmt.Errorf("acquire backup lock: %w", err)
	}
	defer func() { err = errors.Join(err, release()) }()
	return b.settings.Run(ctx, b.db.DB, now)
}
