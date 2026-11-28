// Package db opens and migrates the application's PostgreSQL database.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/17xande-dev/gowebkit/template/postgres/internal/config"
	"github.com/17xande-dev/outbox/pgxstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

//go:embed migrations/*.sql
var files embed.FS

// BackupsNote is the line the admin page shows: the application makes no
// backups of its own on PostgreSQL, and saying "turned off" would mislead.
const BackupsNote = "Backups are made outside the application (pg_dump); see the database guide."

// versionTable is goose's default, named here because the checks below look
// for it directly.
const versionTable = goose.DefaultTablename

// lockWait bounds how long a boot, a status read or a rollback waits for
// another process that is migrating the same database.
//
// It is a variable only so a test can shorten it.
var lockWait = time.Minute

// DB is the migrated connection pool. The caller owns Close.
type DB struct{ pool *pgxpool.Pool }

// Pool exposes the pgx pool to the store.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// Close closes the pool. It returns an error only to match the other variant,
// whose file-backed database can fail to close.
func (d *DB) Close() error {
	d.pool.Close()
	return nil
}

// ExecContext runs one statement on the pool. It is the same method name the
// other variant gets from database/sql, so tests shared by both variants can
// set up or break rows directly; the result is not part of that contract.
func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return d.pool.Exec(ctx, query, args...)
}

// Ping checks whether the database can answer within ctx's deadline.
func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

// LogStartupWarnings flags a likely missing deployment setting before any
// connection attempt, when a failure could obscure the fallback.
func LogStartupWarnings(log *slog.Logger, cfg config.Config) {
	if cfg.CookieSecure && !cfg.DB.URL.IsSet() {
		log.Warn("using the development PostgreSQL URL with COOKIE_SECURE=true; set DATABASE_URL or DATABASE_URL_FILE for this deployment")
	}
}

// Open connects, applies pending migrations and returns the pool. All schema
// changes go through migrations, not startup statements.
func Open(ctx context.Context, cfg config.DB) (*DB, error) {
	return migrate(ctx, cfg, func(ctx context.Context, p *goose.Provider, sqlDB *sql.DB, pool *pgxpool.Pool) error {
		if err := checkHistory(ctx, sqlDB); err != nil {
			return err
		}
		current, target, err := p.GetVersions(ctx)
		if err != nil {
			return err
		}
		if current > target {
			return newerThanBuild(current, target)
		}
		// The application's schema commits first; the queue's tables follow.
		if _, err = p.Up(ctx); err != nil {
			return err
		}
		return pgxstore.Migrate(ctx, pool, nil)
	})
}

// Migrate applies pending migrations without starting the server.
func Migrate(ctx context.Context, cfg config.DB) error {
	d, err := Open(ctx, cfg)
	if err != nil {
		return err
	}
	return d.Close()
}

// MigrateDown rolls back one migration without creating migration history or
// rolling back the baseline, which would delete application data.
func MigrateDown(ctx context.Context, cfg config.DB) error {
	d, err := migrate(ctx, cfg, rollbackOne)
	if err != nil {
		return err
	}
	return d.Close()
}

func rollbackOne(ctx context.Context, p *goose.Provider, sqlDB *sql.DB, _ *pgxpool.Pool) error {
	// Reading versions would create goose's table on a database that has
	// none; a rollback must not change anything.
	if exists, err := versionTableExists(ctx, sqlDB); err != nil {
		return err
	} else if !exists {
		return errors.New("nothing to roll back")
	}
	current, target, err := p.GetVersions(ctx)
	if err != nil {
		return err
	}
	if current > target {
		// goose would only say "version not found" for a migration it
		// does not ship.
		return fmt.Errorf("database migration %d is newer than this build's latest migration %d; run -migrate-down with the image that shipped it", current, target)
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

func newerThanBuild(current, target int64) error {
	return fmt.Errorf("database migration %d is newer than this build's latest migration %d", current, target)
}

// checkHistory refuses a schema that has tables but no migration history:
// guessing what is there is how data is damaged.
func checkHistory(ctx context.Context, sqlDB *sql.DB) error {
	var tables, versioned bool
	err := sqlDB.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' AND table_name <> $1),
		EXISTS (SELECT 1 FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = $1)`, versionTable).Scan(&tables, &versioned)
	if err != nil {
		return fmt.Errorf("inspect database: %w", err)
	}
	if tables && !versioned {
		return fmt.Errorf("the database has tables but no %s table, so its migration history is unknown; "+
			"restore it from a backup or migrate it by hand", versionTable)
	}
	return nil
}

func versionTableExists(ctx context.Context, sqlDB *sql.DB) (bool, error) {
	store, err := database.NewStore(database.DialectPostgres, versionTable)
	if err != nil {
		return false, err
	}
	exists, err := store.(database.StoreExtender).TableExists(ctx, sqlDB)
	if err != nil {
		return false, fmt.Errorf("inspect migration history: %w", err)
	}
	return exists, nil
}

// connect opens a pool and checks that the server answers.
func connect(ctx context.Context, cfg config.DB) (*pgxpool.Pool, error) {
	return connectWithTimeZone(ctx, cfg, "")
}

func connectWithTimeZone(ctx context.Context, cfg config.DB, timeZone string) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.ConnectionString())
	if err != nil {
		// The parser's message can quote the connection string.
		return nil, errors.New("DATABASE_URL is not a valid PostgreSQL connection string")
	}
	// Refuse before connecting: the queue's migration needs two pool
	// connections (its lock and its schema changes), and with one it would
	// wait for the connection it already holds.
	if pc.MaxConns < config.MinPoolConns {
		return nil, errors.New(config.PoolSizeMessage)
	}
	if timeZone != "" {
		pc.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(ctx, `SELECT set_config('TimeZone', $1, false)`, timeZone)
			return err
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	ping, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(ping); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reach the database: %w", err)
	}
	return pool, nil
}

func provider(sqlDB *sql.DB) (*goose.Provider, error) {
	migrations, err := fs.Sub(files, "migrations")
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(goose.DialectPostgres, sqlDB, migrations)
}

// migrate holds the migration lock from before the first query until the step
// has finished, so concurrent boots queue instead of racing to create the same
// tables.
func migrate(ctx context.Context, cfg config.DB, step func(context.Context, *goose.Provider, *sql.DB, *pgxpool.Pool) error) (d *DB, err error) {
	pool, err := connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer func() {
		if d == nil {
			pool.Close()
		}
	}()
	release, err := migrationLock(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer func() {
		if lockErr := release(); lockErr != nil {
			err = errors.Join(err, fmt.Errorf("release migration lock: %w", lockErr))
			d = nil
		}
	}()
	// The adapter belongs to goose's use; closing it leaves the pgx pool open.
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	p, err := provider(sqlDB)
	if err != nil {
		return nil, err
	}
	if err := step(ctx, p, sqlDB, pool); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return &DB{pool}, nil
}

// connectionClass names why a connection failed without any of the text the
// driver wrote, which can quote the user, the host or a server message.
func connectionClass(err error) string {
	var server *pgconn.PgError
	switch {
	case errors.As(err, &server) && len(server.Code) == 5:
		// The server's five-character class, such as 53300 when it has no
		// connection slots left.
		return "SQLSTATE " + server.Code
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "connection failed"
	}
}

// migrationLock takes a session-level advisory lock on a connection of its
// own, waiting at most a minute. The key includes the schema, so a database
// shared by tests that each use a private schema does not serialise them,
// while a deployment (one schema) does. A crashed holder's session ends and
// the server releases the lock.
func migrationLock(ctx context.Context, pool *pgxpool.Pool) (func() error, error) {
	// Keep both slots of a minimum-size pool available for the outbox's own
	// lock and goose connection. This session exists only while migrating.
	connectCtx, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
	defer cancelConnect()
	conn, err := pgx.ConnectConfig(connectCtx, pool.Config().ConnConfig)
	if err != nil {
		// Connection errors may include the user or address, so only a
		// class that cannot carry them is kept.
		return nil, fmt.Errorf("open migration lock connection (%s)", connectionClass(err))
	}
	release := func() error {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return conn.Close(closeCtx)
	}
	const key = `hashtextextended('app.migrate:' || coalesce(current_schema(), ''), 0)`
	wait, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	if _, err := conn.Exec(wait, `SELECT pg_advisory_lock(`+key+`)`); err != nil {
		return nil, errors.Join(fmt.Errorf("acquire migration lock (another process may be migrating this database): %w", err), release())
	}
	return release, nil
}
