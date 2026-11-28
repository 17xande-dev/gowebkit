package db

import (
	"time"

	"github.com/17xande-dev/outbox"
)

// MigrationStatus describes a migration shipped with this build.
type MigrationStatus struct {
	Version   int64
	Applied   bool
	AppliedAt time.Time
	Outbox    bool
}

func appendOutboxMigrations(app []MigrationStatus, queue outbox.MigrationStatus) []MigrationStatus {
	for _, m := range queue.Migrations {
		app = append(app, MigrationStatus{
			Version: m.Version, Applied: m.Applied, AppliedAt: m.AppliedAt, Outbox: true,
		})
	}
	return app
}
