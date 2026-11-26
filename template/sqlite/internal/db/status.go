package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/config"
	"github.com/17xande-dev/sqlitekit/goosestrict"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

// Exists reports whether the database file is there, without creating it or
// its directory. A command that must not initialise an empty database
// (resetting a password) asks this first; opening would create the file.
func Exists(_ context.Context, cfg config.DB) (bool, error) {
	if _, err := os.Stat(cfg.DBPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Status reads migration state without creating a database or version table.
func Status(ctx context.Context, cfg config.DB) (_ []MigrationStatus, err error) {
	path, err := filepath.Abs(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	// A missing parent has no database or lock to inspect yet. Otherwise
	// coordinate with a boot even if it hasn't created the database file.
	_, dirErr := os.Stat(filepath.Dir(path))
	if dirErr == nil {
		release, err := migrationLock(ctx, path)
		if err != nil {
			return nil, err
		}
		defer func() { err = errors.Join(err, release()) }()
	} else if !errors.Is(dirErr, fs.ErrNotExist) {
		return nil, dirErr
	}

	pool, err := openStatusPool(path)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	store, err := goosestrict.NewStore()
	if err != nil {
		return nil, err
	}
	migrations, err := fs.Sub(files, "migrations")
	if err != nil {
		return nil, err
	}
	// Unlike goosestrict.NewProvider, the goose constructor doesn't query
	// the database. It can enumerate sources even when there is no file.
	p, err := goose.NewProvider(goose.DialectCustom, pool, migrations, goose.WithStore(store))
	if err != nil {
		return nil, err
	}
	if dirErr != nil {
		// Report the snapshot from before a concurrent boot could create
		// the directory, rather than querying its new file without the lock.
		return append(pending(p), pendingOutbox), nil
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return append(pending(p), pendingOutbox), nil
	} else if err != nil {
		return nil, err
	}
	exists, err := store.(database.StoreExtender).TableExists(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("inspect migration history: %w", err)
	}
	if !exists {
		return outboxStatus(ctx, pool, pending(p))
	}
	current, target, err := p.GetVersions(ctx)
	if err != nil {
		return nil, err
	}
	if current > target {
		return nil, fmt.Errorf("database migration %d is newer than this build's latest migration %d", current, target)
	}
	statuses, err := p.Status(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]MigrationStatus, 0, len(statuses))
	for _, s := range statuses {
		results = append(results, MigrationStatus{
			Version: s.Source.Version, Applied: s.State == goose.StateApplied, AppliedAt: s.AppliedAt,
		})
	}
	return outboxStatus(ctx, pool, results)
}

func outboxStatus(ctx context.Context, pool *sql.DB, app []MigrationStatus) ([]MigrationStatus, error) {
	store, err := goosestrict.NewStoreTable(outboxHistory)
	if err != nil {
		return nil, err
	}
	return appendOutboxStatus(ctx, pool, store, app)
}

func openStatusPool(path string) (*sql.DB, error) {
	// The writable kit DSN would apply journal_mode(WAL) and create a
	// missing database. Status uses the driver's read-only URI instead.
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro"}
	return sql.Open("sqlite", u.String())
}

func pending(p *goose.Provider) []MigrationStatus {
	sources := p.ListSources()
	results := make([]MigrationStatus, 0, len(sources))
	for _, s := range sources {
		results = append(results, MigrationStatus{Version: s.Version})
	}
	return results
}
