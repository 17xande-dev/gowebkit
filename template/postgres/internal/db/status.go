package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
	"github.com/17xande-dev/outbox/pgxstore"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Exists reports whether the database holds migration history, without
// creating anything. A command that must not initialise an empty database
// (resetting a password) asks this first.
func Exists(ctx context.Context, cfg config.DB) (bool, error) {
	pool, err := connect(ctx, cfg)
	if err != nil {
		return false, err
	}
	defer pool.Close()
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	return versionTableExists(ctx, sqlDB)
}

// Status reads migration state without creating migration history. It holds
// the migration lock while it reads, so it never reports a half-applied boot.
func Status(ctx context.Context, cfg config.DB) (_ []MigrationStatus, err error) {
	// The CLI has always printed the stored clock time with a Z suffix.
	// Read the queue's zone-less history in UTC to preserve that output.
	pool, err := connectWithTimeZone(ctx, cfg, "UTC")
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	release, err := migrationLock(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, release()) }()
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	p, err := provider(sqlDB)
	if err != nil {
		return nil, err
	}
	exists, err := versionTableExists(ctx, sqlDB)
	if err != nil {
		return nil, err
	}
	if !exists {
		sources := p.ListSources()
		pending := make([]MigrationStatus, 0, len(sources))
		for _, s := range sources {
			pending = append(pending, MigrationStatus{Version: s.Version})
		}
		return outboxStatus(ctx, pool, pending)
	}
	current, target, err := p.GetVersions(ctx)
	if err != nil {
		return nil, err
	}
	if current > target {
		return nil, newerThanBuild(current, target)
	}
	statuses, err := p.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("read migration status: %w", err)
	}
	results := make([]MigrationStatus, 0, len(statuses))
	for _, s := range statuses {
		results = append(results, MigrationStatus{
			Version: s.Source.Version, Applied: s.State == goose.StateApplied, AppliedAt: s.AppliedAt,
		})
	}
	return outboxStatus(ctx, pool, results)
}

func outboxStatus(ctx context.Context, pool *pgxpool.Pool, app []MigrationStatus) ([]MigrationStatus, error) {
	queue, err := pgxstore.MigrationStatus(ctx, pool)
	if err != nil {
		return nil, err
	}
	return appendOutboxMigrations(app, queue), nil
}
