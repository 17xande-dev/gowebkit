package db

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
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
		lockPath: cfg.DBPath + ".backup-lock",
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

// Start makes today's backup now, then re-checks on the kit's schedule (hourly,
// retrying a failure within the hour) until the returned loop is stopped.
// Stop the loop before closing the database: it joins any run in progress.
// A nil set (backups disabled) starts nothing and returns a nil loop, whose
// Stop is a no-op.
//
// ctx is the parent context the log calls close over, not the loop's own:
// the loop's context is cancelled by Stop, and Report gets no context.
func (b *BackupSet) Start(ctx context.Context, log *slog.Logger) (*sqlitekit.BackupLoop, error) {
	if b == nil {
		return nil, nil
	}
	return b.settings.Start(ctx, b.db.DB, sqlitekit.BackupSchedule{
		// A separate lock from the migration lock, so a large copy never
		// makes a new server wait to migrate. The kit refuses a name ending
		// in the migration-lock suffix.
		LockPath: b.lockPath,
		Report: func(r sqlitekit.BackupResult) {
			switch {
			case r.Err != nil:
				log.ErrorContext(ctx, "database backup failed", "error", r.Err, "retry_in", r.Next.String())
			case r.Made:
				log.InfoContext(ctx, "database backup saved", "file", filepath.Base(r.Path))
			}
		},
	})
}
