package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/pressly/goose/v3/database"
)

// MigrationStatus describes a migration shipped with this build.
type MigrationStatus struct {
	Version   int64
	Applied   bool
	AppliedAt time.Time
	Outbox    bool
}

const outboxHistory = "outbox_goose_version"

// Outbox v0.1.0 ships just migration 0001 and exposes no status API. Read its
// named history with goose's store without initialising that history.
var pendingOutbox = MigrationStatus{Version: 1, Outbox: true}

func appendOutboxStatus(ctx context.Context, pool *sql.DB, store database.Store, app []MigrationStatus) ([]MigrationStatus, error) {
	row := pendingOutbox
	exists, err := store.(database.StoreExtender).TableExists(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("inspect outbox migration history: %w", err)
	}
	if !exists {
		return append(app, row), nil
	}
	current, err := store.GetLatestVersion(ctx, pool)
	if err != nil && !errors.Is(err, database.ErrVersionNotFound) {
		return nil, fmt.Errorf("read outbox migration version: %w", err)
	}
	if current > row.Version {
		return nil, fmt.Errorf("outbox migration %d is newer than this build's latest migration %d", current, row.Version)
	}
	if current == row.Version {
		version, err := store.GetMigration(ctx, pool, row.Version)
		if err != nil {
			return nil, fmt.Errorf("read outbox migration status: %w", err)
		}
		row.Applied, row.AppliedAt = version.IsApplied, version.Timestamp
	}
	return append(app, row), nil
}
