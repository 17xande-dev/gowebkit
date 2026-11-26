// Package db opens and migrates the application's SQLite database.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/17xande-dev/gowebkit/template/sqlite/internal/config"
	"github.com/17xande-dev/sqlitekit"
	"github.com/17xande-dev/sqlitekit/goosestrict"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var files embed.FS

// DB is the migrated database pool. The caller owns Close.
type DB struct{ *sql.DB }

// LogStartupWarnings has nothing to report for the SQLite configuration.
func LogStartupWarnings(*slog.Logger, config.Config) {}

// Open verifies the SQLite settings and applies migrations before returning.
// All schema changes go through migrations, not startup ALTER statements.
func Open(ctx context.Context, cfg config.DB) (*DB, error) {
	return migrate(ctx, cfg, true, func(ctx context.Context, p *goose.Provider) error {
		_, err := p.Up(ctx)
		return err
	})
}

// Ping checks whether the database can answer within ctx's deadline.
func (d *DB) Ping(ctx context.Context) error { return d.PingContext(ctx) }

// Migrate applies pending migrations without starting the server.
func Migrate(ctx context.Context, cfg config.DB) error {
	d, err := Open(ctx, cfg)
	if err != nil {
		return err
	}
	return d.Close()
}

// MigrateDown rolls back one migration without creating a database or
// rolling back the baseline, which would delete application data.
func MigrateDown(ctx context.Context, cfg config.DB) error {
	d, err := migrate(ctx, cfg, false, rollbackOne)
	if err != nil {
		return err
	}
	return d.Close()
}

func rollbackOne(ctx context.Context, p *goose.Provider) error {
	current, _, err := p.GetVersions(ctx)
	if err != nil {
		return err
	}
	if current == p.ListSources()[0].Version {
		return errors.New("refusing to roll back the baseline migration; restore from a backup instead")
	}
	if _, err := p.Down(ctx); err != nil {
		if errors.Is(err, goose.ErrNoNextVersion) {
			return errors.New("nothing to roll back")
		}
		return err
	}
	return nil
}

func migrate(ctx context.Context, cfg config.DB, create bool, step func(context.Context, *goose.Provider) error) (d *DB, err error) {
	path, err := filepath.Abs(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	opts := sqlitekit.Options{}
	dsn, err := sqlitekit.DSN(path, opts)
	if err != nil {
		return nil, err
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	} else {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no database at %q", filepath.Base(path))
		} else if err != nil {
			return nil, fmt.Errorf("inspect database: %w", err)
		}
		// A rollback must not create a database even if the file disappears
		// between the existence check and the first SQLite connection.
		dsn += "&mode=rw"
	}
	// Even Check can race on a fresh file: the driver's first connection
	// creates the database and switches it to WAL before goose sees it.
	release, err := migrationLock(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if lockErr := release(); lockErr != nil {
			err = errors.Join(err, fmt.Errorf("release migration lock: %w", lockErr))
			if d != nil {
				err = errors.Join(err, d.Close())
				d = nil
			}
		}
	}()

	if create {
		// Set permissions before SQLite or a sidecar starts holding data.
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if err := f.Close(); err != nil {
				return nil, fmt.Errorf("close new database file: %w", err)
			}
		} else if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("create private database file: %w", err)
		}
	}
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer func() {
		if d == nil {
			err = errors.Join(err, pool.Close())
		}
	}()
	if err := sqlitekit.Check(ctx, pool, opts); err != nil {
		return nil, fmt.Errorf("check database settings: %w", err)
	}
	migrations, err := fs.Sub(files, "migrations")
	if err != nil {
		return nil, err
	}
	p, err := goosestrict.NewProvider(pool, migrations)
	if err != nil {
		return nil, err
	}
	if err := step(ctx, p); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return &DB{pool}, nil
}

func migrationLock(ctx context.Context, path string) (func() error, error) {
	wait, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	release, err := sqlitekit.MigrationLock(wait, path)
	if err != nil {
		return nil, fmt.Errorf("acquire migration lock %q: %w", path+".migrate-lock", err)
	}
	return release, nil
}
