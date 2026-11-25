package db

import (
	"context"
	"errors"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
)

// BackupSet is the shape the other variant's file backups take. On PostgreSQL
// the database is a server, not a file, and backups are made with pg_dump
// outside the application, so there is never a set and Backups returns nil.
// The type exists so the shared wiring compiles unchanged for both variants.
type BackupSet struct{}

var errNoBackups = errors.New("the application makes no database backups on PostgreSQL")

// Backups always returns nil: see BackupsNote for what the admin page says.
func (d *DB) Backups(config.DB) *BackupSet { return nil }

// Newest reports no backup; it is safe on the nil set.
func (b *BackupSet) Newest() (time.Time, bool, error) { return time.Time{}, false, nil }

// Run refuses: there is nothing for the application to back up.
func (b *BackupSet) Run(context.Context, time.Time) (string, bool, error) {
	return "", false, errNoBackups
}
