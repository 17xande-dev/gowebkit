package db

import (
	"context"
	"log/slog"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
)

// BackupSet is the shape the other variant's file backups take. On PostgreSQL
// the database is a server, not a file, and backups are made with pg_dump
// outside the application, so there is never a set and Backups returns nil.
// The type exists so the shared wiring compiles unchanged for both variants.
type BackupSet struct{}

// Backups always returns nil: see BackupsNote for what the admin page says.
func (d *DB) Backups(config.DB) *BackupSet { return nil }

// Newest reports no backup; it is safe on the nil set.
func (b *BackupSet) Newest() (time.Time, bool, error) { return time.Time{}, false, nil }

// BackupLoop is the shape of the other variant's running backup schedule.
// There is none here; Stop exists so the shared shutdown wiring compiles.
type BackupLoop struct{}

// Start starts nothing: there is no set, so the loop is always nil.
func (b *BackupSet) Start(context.Context, *slog.Logger) (*BackupLoop, error) { return nil, nil }

// Stop does nothing; it is safe on the nil loop Start returns.
func (l *BackupLoop) Stop() {}
